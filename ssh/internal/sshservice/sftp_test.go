package sshservice

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// startSFTPServer runs an in-process SSH server with an SFTP subsystem that
// serves the real filesystem of the test machine. It returns the target host
// key fingerprint and the listening port.
func startSFTPServer(t *testing.T) (string, int) {
	t.Helper()
	fingerprint, port, _, _ := startSFTPServerKeys(t, false)
	return fingerprint, port
}

func startSFTPServerKeys(t *testing.T, withRSA bool) (string, int, string, *atomic.Int32) {
	return startServer(t, withRSA, false)
}

// startSFTPServerKeys optionally also offers an RSA host key, listed first so
// that default client negotiation picks RSA. It returns the ed25519 and RSA
// fingerprints.
func startServer(t *testing.T, withRSA, publicKeyOnly bool) (string, int, string, *atomic.Int32) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	rsaFingerprint := ""
	connections := &atomic.Int32{}
	config := &gossh.ServerConfig{PasswordCallback: func(c gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
		if c.User() == "agent" && string(password) == "test-only" {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	if withRSA {
		rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		rsaSigner, err := gossh.NewSignerFromKey(rsaKey)
		if err != nil {
			t.Fatal(err)
		}
		config.AddHostKey(rsaSigner)
		rsaFingerprint = gossh.FingerprintSHA256(rsaSigner.PublicKey())
	}
	if publicKeyOnly {
		config.PasswordCallback = nil
		config.PublicKeyCallback = func(gossh.ConnMetadata, gossh.PublicKey) (*gossh.Permissions, error) {
			return nil, fmt.Errorf("denied")
		}
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go serveSSH(conn, config)
		}
	}()
	return gossh.FingerprintSHA256(signer.PublicKey()), listener.Addr().(*net.TCPAddr).Port, rsaFingerprint, connections
}

func serveSSH(conn net.Conn, config *gossh.ServerConfig) {
	defer conn.Close()
	serverConn, channels, requests, err := gossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go gossh.DiscardRequests(requests)
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(gossh.UnknownChannelType, "unsupported")
			continue
		}
		channel, requests, err := incoming.Accept()
		if err != nil {
			continue
		}
		go func() {
			for request := range requests {
				if request.Type == "subsystem" && len(request.Payload) >= 8 && string(request.Payload[4:]) == "sftp" {
					_ = request.Reply(true, nil)
					server, err := sftp.NewServer(channel)
					if err == nil {
						_ = server.Serve()
					}
					_ = channel.Close()
					return
				}
				_ = request.Reply(false, nil)
			}
		}()
	}
}

type sftpFixture struct {
	service *Service
	root    string
	outside string
}

func newSFTPFixture(t *testing.T, publishRoot func(root string) string) *sftpFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "www")
	outside := filepath.Join(base, "secret")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, port := startSFTPServer(t)
	store := &MemoryTargetStore{SnapshotValue: TargetSnapshot{Revision: "rev-1", Targets: []Target{
		{ID: "web", Label: "Web", Host: "127.0.0.1", Port: port, Username: "agent", HostKeyFingerprint: fingerprint, CredentialRef: "k", Enabled: true, Published: true, PublishRoot: publishRoot(root)},
		{ID: "plain", Label: "Plain", Host: "127.0.0.1", Port: port, Username: "agent", HostKeyFingerprint: fingerprint, CredentialRef: "k", Enabled: true, Published: true},
		{ID: "wrongkey", Label: "Wrong", Host: "127.0.0.1", Port: port, Username: "agent", HostKeyFingerprint: "SHA256:AAAA", CredentialRef: "k", Enabled: true, Published: true, PublishRoot: publishRoot(root)},
	}}}
	provider := &MemoryCredentialProvider{Credentials: map[string]Credential{"k": {Method: "password", Password: "test-only"}}}
	connector := NewSSHConnector()
	connector.allowLoopback = true
	service, err := New(DefaultConfig(), store, provider, connector)
	if err != nil {
		t.Fatal(err)
	}
	return &sftpFixture{service: service, root: root, outside: outside}
}

func rootOf(root string) string { return root }

func TestUploadListRemoveRoundTrip(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	ctx := context.Background()
	content := []byte("<h1>hello</h1>")
	sum := sha256.Sum256(content)
	result, err := f.service.Upload(ctx, "web", "site/index.html", content, false, "0644")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.Path != "site/index.html" || result.Bytes != len(content) || result.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected result: %#v", result)
	}
	written, err := os.ReadFile(filepath.Join(f.root, "site", "index.html"))
	if err != nil || !bytes.Equal(written, content) {
		t.Fatalf("remote file mismatch: %q %v", written, err)
	}
	info, _ := os.Stat(filepath.Join(f.root, "site", "index.html"))
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("unexpected mode %v", info.Mode())
	}
	listed, err := f.service.List(ctx, "web", "site")
	if err != nil || len(listed.Entries) != 1 || listed.Entries[0].Path != "site/index.html" || listed.Entries[0].Bytes != int64(len(content)) || listed.Entries[0].Mode != "0644" {
		t.Fatalf("unexpected list: %#v %v", listed, err)
	}
	rootList, err := f.service.List(ctx, "web", "")
	if err != nil || len(rootList.Entries) != 1 || !rootList.Entries[0].Dir {
		t.Fatalf("unexpected root list: %#v %v", rootList, err)
	}
	removed, err := f.service.Remove(ctx, "web", "site/index.html")
	if err != nil || !removed.Removed {
		t.Fatalf("unexpected remove: %#v %v", removed, err)
	}
	removed, err = f.service.Remove(ctx, "web", "site/index.html")
	if err != nil || removed.Removed || removed.Status != "ok" {
		t.Fatalf("second remove should report removed=false: %#v %v", removed, err)
	}
	removed, err = f.service.Remove(ctx, "web", "nope/missing.html")
	if err != nil || removed.Removed {
		t.Fatalf("missing parent should report removed=false: %#v %v", removed, err)
	}
	_, err = f.service.Remove(ctx, "web", "site")
	assertCode(t, err, "not_a_file")
}

func TestUploadRejectsUnsafePaths(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	for _, path := range []string{"../escape.txt", "a/../../escape.txt", "a/..", "/etc/passwd", "", ".", "a\\b", "a/\x00b", strings.Repeat("a", 2000), tempPrefix + "x.tmp"} {
		_, err := f.service.Upload(context.Background(), "web", path, []byte("x"), true, "")
		assertCode(t, err, "invalid_path")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.root), "escape.txt")); err == nil {
		t.Fatal("file escaped the publication root")
	}
	_, err := f.service.Remove(context.Background(), "web", "../secret/x")
	assertCode(t, err, "invalid_path")
	_, err = f.service.List(context.Background(), "web", "/")
	assertCode(t, err, "invalid_path")
}

func TestSymlinksCannotLeavePublishRoot(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	ctx := context.Background()
	secret := filepath.Join(f.outside, "keep.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.outside, filepath.Join(f.root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(f.root, "filelink")); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.Upload(ctx, "web", "dirlink/new.txt", []byte("x"), true, "")
	assertCode(t, err, "path_outside_root")
	_, err = f.service.Upload(ctx, "web", "dirlink/deep/new.txt", []byte("x"), true, "")
	assertCode(t, err, "path_outside_root")
	_, err = f.service.Upload(ctx, "web", "filelink", []byte("x"), true, "")
	assertCode(t, err, "path_outside_root")
	_, err = f.service.List(ctx, "web", "dirlink")
	assertCode(t, err, "path_outside_root")
	_, err = f.service.Remove(ctx, "web", "dirlink/keep.txt")
	assertCode(t, err, "path_outside_root")
	if data, err := os.ReadFile(secret); err != nil || string(data) != "secret" {
		t.Fatalf("outside file was modified: %q %v", data, err)
	}
	if entries, _ := os.ReadDir(f.outside); len(entries) != 1 {
		t.Fatalf("outside directory was modified: %v", entries)
	}
}

func TestPublishNotEnabled(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	ctx := context.Background()
	_, err := f.service.Upload(ctx, "plain", "a.txt", []byte("x"), false, "")
	assertCode(t, err, "publish_not_enabled")
	_, err = f.service.Remove(ctx, "plain", "a.txt")
	assertCode(t, err, "publish_not_enabled")
	_, err = f.service.List(ctx, "plain", "")
	assertCode(t, err, "publish_not_enabled")
	_, err = f.service.Upload(ctx, "missing", "a.txt", []byte("x"), false, "")
	assertCode(t, err, "ssh_target_unknown")
	targets, _ := f.service.Targets(ctx)
	for _, target := range targets.Targets {
		if target.ID == "web" && !target.PublishEnabled || target.ID == "plain" && target.PublishEnabled {
			t.Fatalf("publish_enabled projection is wrong: %#v", target)
		}
	}
}

func TestOverwriteModeAndSizeLimits(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	ctx := context.Background()
	if _, err := f.service.Upload(ctx, "web", "a.txt", []byte("one"), false, ""); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.Upload(ctx, "web", "a.txt", []byte("two"), false, "")
	assertCode(t, err, "file_exists")
	if data, _ := os.ReadFile(filepath.Join(f.root, "a.txt")); string(data) != "one" {
		t.Fatalf("existing file was modified: %q", data)
	}
	if _, err := f.service.Upload(ctx, "web", "a.txt", []byte("two"), true, "0600"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(f.root, "a.txt"))
	info, _ := os.Stat(filepath.Join(f.root, "a.txt"))
	if string(data) != "two" || info.Mode().Perm() != 0o600 {
		t.Fatalf("overwrite failed: %q %v", data, info.Mode())
	}
	for _, mode := range []string{"0200", "0000", "0400", "0044"} {
		_, err = f.service.Upload(ctx, "web", "m-"+mode+".txt", []byte("x"), false, mode)
		assertCode(t, err, "invalid_arguments")
		if _, statErr := os.Stat(filepath.Join(f.root, "m-"+mode+".txt")); statErr == nil {
			t.Fatalf("mode %s: a file was written", mode)
		}
	}
	_, err = f.service.Upload(ctx, "web", "big.bin", make([]byte, MaxUploadBytes+1), true, "")
	assertCode(t, err, "file_too_large")
	if _, err := f.service.Upload(ctx, "web", "max.bin", make([]byte, MaxUploadBytes), true, ""); err != nil {
		t.Fatalf("file at the limit must be accepted: %v", err)
	}
	_, err = f.service.Upload(ctx, "web", "m.txt", []byte("x"), true, "4755")
	assertCode(t, err, "invalid_arguments")
	_, err = f.service.Upload(ctx, "web", "m.txt", []byte("x"), true, "rwx")
	assertCode(t, err, "invalid_arguments")
	assertNoTemp(t, f.root)
}

func TestUnknownHostKeyIsRefused(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	_, err := f.service.Upload(context.Background(), "wrongkey", "a.txt", []byte("x"), true, "")
	assertCode(t, err, "ssh_host_key_mismatch")
	_, err = f.service.List(context.Background(), "wrongkey", "")
	assertCode(t, err, "ssh_host_key_mismatch")
	if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
		t.Fatalf("nothing must be written on host key mismatch: %v", entries)
	}
}

func TestListIsBounded(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	for i := 0; i < MaxListEntries+20; i++ {
		if err := os.WriteFile(filepath.Join(f.root, "f"+strconv.Itoa(1000+i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.root, tempPrefix+"abc"+tempSuffix), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	listed, err := f.service.List(context.Background(), "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != MaxListEntries || !listed.Truncated {
		t.Fatalf("list not bounded: %d truncated=%v", len(listed.Entries), listed.Truncated)
	}
	for _, entry := range listed.Entries {
		if strings.HasPrefix(entry.Path, tempPrefix) {
			t.Fatal("temporary upload files must be hidden")
		}
	}
	_, err = f.service.List(context.Background(), "web", "absent")
	assertCode(t, err, "path_not_found")
}

func TestUploadIsAtomicForReaders(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	ctx := context.Background()
	oldContent := bytes.Repeat([]byte("old-"), 1<<20)
	newContent := bytes.Repeat([]byte("new!"), 1<<20)
	final := filepath.Join(f.root, "page.html")
	var torn atomic.Int32
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(final)
			if err != nil {
				continue // not visible yet: allowed
			}
			if !bytes.Equal(data, oldContent) && !bytes.Equal(data, newContent) {
				torn.Add(1)
			}
		}
	}()
	// Brand new file: either absent or complete, never partial.
	if _, err := f.service.Upload(ctx, "web", "page.html", oldContent, false, ""); err != nil {
		t.Fatal(err)
	}
	// Replacement: either old or new, never partial.
	if _, err := f.service.Upload(ctx, "web", "page.html", newContent, true, ""); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	if torn.Load() != 0 {
		t.Fatalf("a partial file was visible %d times", torn.Load())
	}
	assertNoTemp(t, f.root)
}

func assertNoTemp(t *testing.T, root string) {
	t.Helper()
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempPrefix) {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestValidateSnapshotPublishRoot(t *testing.T) {
	base := Target{ID: "web", Label: "w", Host: "h", Port: 22, Username: "u", CredentialRef: "c"}
	for _, root := range []string{"relative", "/", "/a/../b", "/a//b", "/a/"} {
		target := base
		target.PublishRoot = root
		if err := validateSnapshot(&TargetSnapshot{Targets: []Target{target}}); err == nil {
			t.Fatalf("publish_root %q must be rejected", root)
		}
	}
	target := base
	target.PublishRoot = "/var/www/site"
	if err := validateSnapshot(&TargetSnapshot{Targets: []Target{target}}); err != nil {
		t.Fatal(err)
	}
}

func TestSFTPUnavailableOnNonSFTPConnection(t *testing.T) {
	service, store, _ := testService(t, ModeRun)
	store.SnapshotValue.Targets[0].PublishRoot = "/srv/www"
	_, err := service.Upload(context.Background(), "prod", "a.txt", []byte("x"), false, "")
	assertCode(t, err, "ssh_sftp_unavailable")
}

func TestOverwriteWithoutPosixRenameIsRefused(t *testing.T) {
	f := newSFTPFixture(t, rootOf)
	ctx := context.Background()
	if _, err := f.service.Upload(ctx, "web", "a.txt", []byte("one"), false, ""); err != nil {
		t.Fatal(err)
	}
	posixRenameExtension = "absent@example.invalid"
	defer func() { posixRenameExtension = "posix-rename@openssh.com" }()
	_, err := f.service.Upload(ctx, "web", "a.txt", []byte("two"), true, "")
	assertCode(t, err, "atomic_replace_unavailable")
	if data, _ := os.ReadFile(filepath.Join(f.root, "a.txt")); string(data) != "one" {
		t.Fatalf("existing file was touched: %q", data)
	}
	assertNoTemp(t, f.root)
	// overwrite:false keeps the plain Rename.
	if _, err := f.service.Upload(ctx, "web", "b.txt", []byte("new"), false, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedHostKeyTypeWhenServerOffersSeveralKeys(t *testing.T) {
	edFingerprint, port, rsaFingerprint, _ := startSFTPServerKeys(t, true)
	connector := NewSSHConnector()
	connector.allowLoopback = true
	credential := Credential{Method: "password", Password: "test-only"}
	base := Target{ID: "web", Host: "127.0.0.1", Port: port, Username: "agent"}
	cases := []struct {
		name, fingerprint, keyType string
		ok                         bool
	}{
		{"ed25519 pinned, type unknown", edFingerprint, "", true},
		{"ed25519 pinned, type set", edFingerprint, "ssh-ed25519", true},
		{"rsa pinned, type unknown", rsaFingerprint, "", true},
		{"rsa pinned, type set", rsaFingerprint, "rsa", true},
		{"ed25519 pinned with rsa type", edFingerprint, "rsa", false},
		{"unknown fingerprint", "SHA256:AAAA", "", false},
	}
	for _, c := range cases {
		target := base
		target.HostKeyFingerprint, target.HostKeyType = c.fingerprint, c.keyType
		connection, err := connector.Connect(context.Background(), target, credential, 5*time.Second)
		if c.ok {
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			_ = connection.Close()
			continue
		}
		assertCode(t, err, "ssh_host_key_mismatch")
	}
}

// A server-side failure after authentication started must never trigger a new
// attempt, whatever text the server sends.
func TestNoRetryAfterAuthenticationStarted(t *testing.T) {
	edFingerprint, port, _, connections := startSFTPServerKeys(t, true)
	connector := NewSSHConnector()
	connector.allowLoopback = true
	target := Target{ID: "web", Host: "127.0.0.1", Port: port, Username: "agent", HostKeyFingerprint: edFingerprint, HostKeyType: "ssh-ed25519"}
	_, err := connector.Connect(context.Background(), target, Credential{Method: "password", Password: "wrong"}, 5*time.Second)
	assertCode(t, err, "ssh_authentication_failed")
	if got := connections.Load(); got != 1 {
		t.Fatalf("expected one attempt after authentication started, got %d", got)
	}
	// Without a configured type: default negotiation picks RSA (rejected by our
	// callback), the ed25519 attempt reaches authentication and fails there:
	// exactly two attempts, no further family is tried.
	target.HostKeyType = ""
	_, err = connector.Connect(context.Background(), target, Credential{Method: "password", Password: "wrong"}, 5*time.Second)
	assertCode(t, err, "ssh_authentication_failed")
	if got := connections.Load(); got != 3 {
		t.Fatalf("expected two more attempts (3 total), got %d", got)
	}
}

// Once our callback accepted the key, a failure that follows (here: the server
// only accepts publickey while we hold a password, so no auth callback runs)
// must not trigger any further attempt.
func TestNoRetryAfterHostKeyAccepted(t *testing.T) {
	edFingerprint, port, _, connections := startServer(t, true, true)
	connector := NewSSHConnector()
	connector.allowLoopback = true
	target := Target{ID: "web", Host: "127.0.0.1", Port: port, Username: "agent", HostKeyFingerprint: edFingerprint}
	_, err := connector.Connect(context.Background(), target, Credential{Method: "password", Password: "x"}, 5*time.Second)
	assertCode(t, err, "ssh_authentication_failed")
	if got := connections.Load(); got != 2 {
		t.Fatalf("expected RSA rejected then ed25519 accepted (2 attempts), got %d", got)
	}
}
