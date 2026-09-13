package sshservice

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestStrictHostKeyAndBoundedWriter(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := gossh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := gossh.FingerprintSHA256(publicKey)
	if err := strictHostKey(fingerprint)("fixture", &net.TCPAddr{}, publicKey); err != nil {
		t.Fatalf("matching host key rejected: %v", err)
	}
	if err := strictHostKey("SHA256:wrong")("fixture", &net.TCPAddr{}, publicKey); err == nil {
		t.Fatal("mismatched host key accepted")
	}

	writer := &boundedWriter{limit: 4}
	if n, err := writer.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("unexpected bounded write: %d %v", n, err)
	}
	if writer.String() != "abcd" || !writer.Truncated() {
		t.Fatalf("unexpected bounded writer state: %q %v", writer.String(), writer.Truncated())
	}
	if shellQuote("a'b") != "'a'\\''b'" {
		t.Fatalf("unexpected shell quote: %s", shellQuote("a'b"))
	}
}

func TestForbiddenIP(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "::1", "169.254.169.254", "0.0.0.0", "224.0.0.1"} {
		if !forbiddenIP(net.ParseIP(value)) {
			t.Fatalf("expected %s to be forbidden", value)
		}
	}
	for _, value := range []string{"10.20.0.12", "192.168.1.2", "8.8.8.8"} {
		if forbiddenIP(net.ParseIP(value)) {
			t.Fatalf("expected %s to be allowed", value)
		}
	}
}
