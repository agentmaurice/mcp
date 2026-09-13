package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewDefaultConfig(t *testing.T) {
	cfg := NewDefaultConfig()
	require.Equal(t, "custom", cfg.MCPType)
	require.Equal(t, "/var/mcp/credentials", cfg.CredentialsPath)
	require.Equal(t, LocalMCPTransportSTDIO, cfg.LocalMCPTransport)
	require.Equal(t, 3, cfg.RetryAttempts)
	require.Equal(t, "info", cfg.LogLevel)
}

func TestValidateErrors(t *testing.T) {
	cfg := &SidecarConfig{}
	require.ErrorContains(t, cfg.Validate(), "maurice_url is required")

	cfg = &SidecarConfig{
		MauriceURL:  "https://example.com",
		MQTTBroker:  "tls://broker:8883",
		MQTTTimeout: 1 * time.Second,
	}
	require.ErrorContains(t, cfg.Validate(), "deployment_id is required when mqtt_broker is configured")

	cfg = &SidecarConfig{
		MauriceURL:      "https://example.com",
		DeploymentID:    "dep-1",
		MQTTBroker:      "http://broker:1883",
		LocalMCPCommand: "mcp",
	}
	require.ErrorContains(t, cfg.Validate(), "mqtt_broker must use one of")

	cfg = &SidecarConfig{
		MauriceURL:      "https://example.com",
		DeploymentID:    "dep-1",
		MQTTBroker:      "tcp://broker:1883",
		LocalMCPCommand: "mcp",
	}
	require.NoError(t, cfg.Validate())

	cfg = &SidecarConfig{
		MauriceURL:      "https://example.com",
		DeploymentID:    "dep-1",
		MQTTBroker:      "tls://broker:8883",
		LocalMCPCommand: " ",
	}
	require.ErrorContains(t, cfg.Validate(), "local_mcp_command is required when mqtt_broker is configured")

	cfg = &SidecarConfig{
		MauriceURL:   "https://example.com",
		DeploymentID: "dep-1",
		MQTTBroker:   "tls://broker:8883",
		RuntimeSpec:  `{"runtime_kind":"python","source":{"repo_url":"https://github.com/owner/repo","ref":"v1.0.0"},"start_command":"python main.py"}`,
	}
	require.NoError(t, cfg.Validate())

	cfg = &SidecarConfig{
		MauriceURL:        "https://example.com",
		LocalMCPTransport: LocalMCPTransportHTTP,
	}
	require.ErrorContains(t, cfg.Validate(), "no longer supported")

	cfg = &SidecarConfig{
		MauriceURL:         "https://example.com",
		MQTTClientCertFile: "/tmp/cert.pem",
	}
	require.ErrorContains(t, cfg.Validate(), "must be set together")
}

func TestValidateSetsDefaultRuntimeValues(t *testing.T) {
	cfg := &SidecarConfig{
		MauriceURL:               "https://example.com",
		DeploymentID:             "dep-1",
		MQTTBroker:               "tls://broker:8883",
		LocalMCPCommand:          "mcp",
		MQTTTimeout:              0,
		CommandTimeout:           -1,
		HeartbeatInterval:        0,
		MQTTMaxPayloadBytes:      0,
		MQTTMaxDecompressedBytes: -1,
		MCPRestartMax:            -10,
		MCPRestartBackoffInitial: 0,
		MCPRestartBackoffMax:     -1,
		RuntimeWorkDir:           "",
		BootstrapTimeout:         0,
	}
	require.NoError(t, cfg.Validate())
	require.Equal(t, 2*time.Minute, cfg.MQTTTimeout)
	require.Equal(t, 2*time.Minute, cfg.CommandTimeout)
	require.Equal(t, 30*time.Second, cfg.HeartbeatInterval)
	require.Equal(t, 1<<20, cfg.MQTTMaxPayloadBytes)
	require.Equal(t, 4<<20, cfg.MQTTMaxDecompressedBytes)
	require.Equal(t, 0, cfg.MCPRestartMax)
	require.Equal(t, time.Second, cfg.MCPRestartBackoffInitial)
	require.Equal(t, 30*time.Second, cfg.MCPRestartBackoffMax)
	require.Equal(t, "/tmp/mcp-sidecar-workspace", cfg.RuntimeWorkDir)
	require.Equal(t, 10*time.Minute, cfg.BootstrapTimeout)
}

func TestValidateForRegistration(t *testing.T) {
	cfg := &SidecarConfig{MauriceURL: "https://example.com"}
	require.ErrorContains(t, cfg.ValidateForRegistration(), "bootstrap_token is required for registration")

	cfg.BootstrapToken = "bootstrap-token"
	require.NoError(t, cfg.ValidateForRegistration())
}

func TestTLSConfigBasicAndErrors(t *testing.T) {
	cfg := &SidecarConfig{
		MQTTInsecureSkipVerify: true,
		MQTTServerName:         " broker.local ",
	}
	tlsCfg, err := cfg.TLSConfig()
	require.NoError(t, err)
	require.True(t, tlsCfg.InsecureSkipVerify)
	require.Equal(t, "broker.local", tlsCfg.ServerName)

	cfg.MQTTCAFile = filepath.Join(t.TempDir(), "missing.pem")
	_, err = cfg.TLSConfig()
	require.ErrorContains(t, err, "read mqtt_ca_file")

	dir := t.TempDir()
	badCA := filepath.Join(dir, "bad-ca.pem")
	require.NoError(t, os.WriteFile(badCA, []byte("not-a-certificate"), 0600))
	cfg.MQTTCAFile = badCA
	_, err = cfg.TLSConfig()
	require.ErrorContains(t, err, "invalid certificate in mqtt_ca_file")
}

func TestTLSConfigWithClientCertificate(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := mustGenerateSelfSignedCertPEM(t)

	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "client-cert.pem")
	keyPath := filepath.Join(dir, "client-key.pem")
	require.NoError(t, os.WriteFile(caPath, certPEM, 0600))
	require.NoError(t, os.WriteFile(certPath, certPEM, 0600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0600))

	cfg := &SidecarConfig{
		MQTTCAFile:         caPath,
		MQTTClientCertFile: certPath,
		MQTTClientKeyFile:  keyPath,
	}

	tlsCfg, err := cfg.TLSConfig()
	require.NoError(t, err)
	require.NotNil(t, tlsCfg.RootCAs)
	require.Len(t, tlsCfg.Certificates, 1)
}

func mustGenerateSelfSignedCertPEM(t *testing.T) ([]byte, []byte) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM
}
