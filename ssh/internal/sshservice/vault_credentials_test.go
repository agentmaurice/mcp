package sshservice_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentmaurice/mcpchatui/mcp/ssh/internal/sshservice"
	"golang.org/x/crypto/hkdf"
)

func TestVaultCredentialProviderResolveOneShot(t *testing.T) {
	consumer, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Keep this fixture's runtime bytes stable while preventing publication
	// scanners from treating the deliberately fake value as a private key.
	const (
		openSSHKeyBegin = "-----BEGIN " + "OPENSSH PRIVATE KEY-----"
		openSSHKeyEnd   = "-----END " + "OPENSSH PRIVATE KEY-----"
	)
	secret := []byte(openSSHKeyBegin + "\nvault-test\n" + openSSHKeyEnd)
	blob, err := rewrapForTest(secret, consumer.PublicKey())
	if err != nil {
		t.Fatal(err)
	}

	var consumed int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relay/dlv_act/consume" {
			http.NotFound(w, r)
			return
		}
		consumed++
		if consumed > 1 {
			http.Error(w, "grant_exhausted", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"delivery_id": "dlv_act",
			"blob_b64":    blob,
			"one_shot":    true,
		})
	}))
	defer ts.Close()

	provider, err := sshservice.NewVaultCredentialProvider(ts.URL, base64.StdEncoding.EncodeToString(consumer.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := provider.Resolve(context.Background(), "dlv_act", sshservice.Purpose{TargetID: "t1", Mode: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if cred.Method != "private_key" || cred.PrivateKey != string(secret) {
		t.Fatalf("%+v", cred)
	}
	if _, err := provider.Resolve(context.Background(), "dlv_act", sshservice.Purpose{}); err == nil {
		t.Fatal("expected second consume failure")
	}
}

func rewrapForTest(plaintext []byte, consumerPub *ecdh.PublicKey) (string, error) {
	sender, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := sender.ECDH(consumerPub)
	if err != nil {
		return "", err
	}
	r := hkdf.New(sha256.New, shared, nil, []byte("agentmaurice.vault.relay/v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	raw, err := json.Marshal(map[string]string{
		"sender_pub_b64": base64.StdEncoding.EncodeToString(sender.PublicKey().Bytes()),
		"nonce_b64":      base64.StdEncoding.EncodeToString(nonce),
		"ciphertext_b64": base64.StdEncoding.EncodeToString(ct),
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
