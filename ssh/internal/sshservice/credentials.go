package sshservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
)

type UnavailableCredentialProvider struct{}

func (UnavailableCredentialProvider) Available() bool { return false }

func (UnavailableCredentialProvider) Resolve(context.Context, string, Purpose) (Credential, error) {
	return Credential{}, NewError("credentials_provider_unavailable", "no credential provider is configured")
}

type FileCredentialProvider struct {
	path string
}

func NewFileCredentialProvider(path string) CredentialProvider {
	path = strings.TrimSpace(path)
	if path == "" {
		return UnavailableCredentialProvider{}
	}
	return &FileCredentialProvider{path: path}
}

func (p *FileCredentialProvider) Available() bool {
	info, err := os.Stat(p.path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0
}

func (p *FileCredentialProvider) Resolve(_ context.Context, ref string, _ Purpose) (Credential, error) {
	info, err := os.Stat(p.path)
	if err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "credential provider cannot resolve the requested reference")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Credential{}, NewError("ssh_credential_unavailable", "credential file permissions are too broad")
	}
	payload, err := os.ReadFile(p.path)
	if err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "credential provider cannot read the requested reference")
	}
	var document struct {
		Credentials map[string]Credential `json:"credentials"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "credential provider data is invalid")
	}
	credential, ok := document.Credentials[ref]
	if !ok {
		return Credential{}, NewError("ssh_credential_unavailable", "credential reference is unavailable")
	}
	if err := validateCredential(credential); err != nil {
		return Credential{}, NewError("ssh_credential_unavailable", "%v", err)
	}
	return credential, nil
}

func validateCredential(credential Credential) error {
	switch credential.Method {
	case "private_key":
		if strings.TrimSpace(credential.PrivateKey) == "" {
			return fmt.Errorf("private key credential is incomplete")
		}
	case "password":
		if credential.Password == "" {
			return fmt.Errorf("password credential is incomplete")
		}
	default:
		return fmt.Errorf("unsupported credential method")
	}
	return nil
}

type MemoryCredentialProvider struct {
	Credentials map[string]Credential
	Err         error
}

func (p *MemoryCredentialProvider) Available() bool { return p != nil }

func (p *MemoryCredentialProvider) Resolve(_ context.Context, ref string, _ Purpose) (Credential, error) {
	if p == nil {
		return Credential{}, NewError("credentials_provider_unavailable", "no credential provider is configured")
	}
	if p.Err != nil {
		return Credential{}, p.Err
	}
	credential, ok := p.Credentials[ref]
	if !ok {
		return Credential{}, NewError("ssh_credential_unavailable", "credential reference is unavailable")
	}
	return credential, nil
}
