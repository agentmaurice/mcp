package sshservice

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

const vaultRelayInfo = "agentmaurice.vault.relay/v1"

// VaultCredentialProvider resolves credential_ref as a vault sealed-relay delivery_id.
// The process holds the ephemeral X25519 private key; the vault HTTP API only sees ciphertext.
type VaultCredentialProvider struct {
	BaseURL      string
	HTTPClient   *http.Client
	ConsumerPriv *ecdh.PrivateKey
}

func NewVaultCredentialProvider(baseURL, consumerPrivB64 string) (CredentialProvider, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return UnavailableCredentialProvider{}, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(consumerPrivB64))
	if err != nil {
		return nil, fmt.Errorf("SSH_VAULT_CONSUMER_PRIV_B64: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("SSH_VAULT_CONSUMER_PRIV_B64: %w", err)
	}
	return &VaultCredentialProvider{
		BaseURL:      baseURL,
		HTTPClient:   &http.Client{Timeout: 15 * time.Second},
		ConsumerPriv: priv,
	}, nil
}

// GenerateVaultConsumerKey returns a fresh ephemeral pair for operators to publish the public half.
func GenerateVaultConsumerKey() (pubB64, privB64 string, err error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()),
		base64.StdEncoding.EncodeToString(priv.Bytes()), nil
}

func (p *VaultCredentialProvider) Available() bool {
	return p != nil && p.BaseURL != "" && p.ConsumerPriv != nil
}

func (p *VaultCredentialProvider) PublicB64() string {
	if p == nil || p.ConsumerPriv == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(p.ConsumerPriv.PublicKey().Bytes())
}

func (p *VaultCredentialProvider) Resolve(ctx context.Context, ref string, _ Purpose) (Credential, error) {
	if !p.Available() {
		return Credential{}, NewError("credentials_provider_unavailable", "vault credential provider is not configured")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Credential{}, NewError("ssh_credential_unavailable", "credential reference is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v1/relay/"+ref+"/consume", nil)
	if err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "vault request failed")
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "vault unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return Credential{}, NewError("ssh_credential_unavailable", "vault grant exhausted")
	}
	if resp.StatusCode != http.StatusOK {
		return Credential{}, NewError("ssh_credential_unavailable", "vault consume status %d", resp.StatusCode)
	}
	var body struct {
		BlobB64 string `json:"blob_b64"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.BlobB64 == "" {
		return Credential{}, NewError("ssh_credential_unavailable", "vault consume payload invalid")
	}
	plain, err := openVaultRelayBlob(body.BlobB64, p.ConsumerPriv)
	if err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "vault blob decrypt failed")
	}
	cred := Credential{Method: "private_key"}
	if strings.HasPrefix(strings.TrimSpace(string(plain)), "{") {
		var env struct {
			PrivateKey string `json:"private_key"`
			Passphrase string `json:"passphrase"`
			Password   string `json:"password"`
			Method     string `json:"method"`
		}
		if err := json.Unmarshal(plain, &env); err == nil {
			if env.Method == "password" || env.Password != "" {
				cred.Method = "password"
				cred.Password = env.Password
				wipeBytes(plain)
				return cred, nil
			}
			if env.PrivateKey != "" {
				cred.PrivateKey = env.PrivateKey
				cred.Passphrase = env.Passphrase
				wipeBytes(plain)
				return cred, nil
			}
		}
	}
	cred.PrivateKey = string(plain)
	wipeBytes(plain)
	if err := validateCredential(cred); err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "%v", err)
	}
	return cred, nil
}

type vaultRelayBlob struct {
	SenderPubB64  string `json:"sender_pub_b64"`
	NonceB64      string `json:"nonce_b64"`
	CiphertextB64 string `json:"ciphertext_b64"`
}

func openVaultRelayBlob(blobB64 string, consumerPriv *ecdh.PrivateKey) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(blobB64)
	if err != nil {
		return nil, err
	}
	var blob vaultRelayBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return nil, err
	}
	senderRaw, err := base64.StdEncoding.DecodeString(blob.SenderPubB64)
	if err != nil {
		return nil, err
	}
	senderPub, err := ecdh.X25519().NewPublicKey(senderRaw)
	if err != nil {
		return nil, err
	}
	shared, err := consumerPriv.ECDH(senderPub)
	if err != nil {
		return nil, err
	}
	r := hkdf.New(sha256.New, shared, nil, []byte(vaultRelayInfo))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(blob.CiphertextB64)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(blob.NonceB64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ct, nil)
}

func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
