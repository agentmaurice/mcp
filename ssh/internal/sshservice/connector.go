package sshservice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

type SSHConnector struct {
	dialer   net.Dialer
	resolver *net.Resolver
	// allowLoopback is for in-process test servers only.
	allowLoopback bool
}

func NewSSHConnector() *SSHConnector { return &SSHConnector{resolver: net.DefaultResolver} }

// hostKeyAlgorithmSets maps a pinned host key type to the signature algorithms
// that can present that key. A SHA256 fingerprint carries no key type, so
// without a configured type Connect tries the default negotiation first, then
// one restricted attempt per key family, and the fingerprint check still
// decides every attempt.
var hostKeyAlgorithmSets = map[string][]string{
	"ssh-ed25519":         {gossh.KeyAlgoED25519},
	"rsa":                 {gossh.KeyAlgoRSASHA512, gossh.KeyAlgoRSASHA256, gossh.KeyAlgoRSA},
	"ecdsa-sha2-nistp256": {gossh.KeyAlgoECDSA256},
	"ecdsa-sha2-nistp384": {gossh.KeyAlgoECDSA384},
	"ecdsa-sha2-nistp521": {gossh.KeyAlgoECDSA521},
}

var hostKeyFamilyOrder = []string{"ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "rsa"}

func validHostKeyType(value string) bool {
	_, ok := hostKeyAlgorithmSets[value]
	return value == "" || ok
}

func (c *SSHConnector) Connect(ctx context.Context, target Target, credential Credential, timeout time.Duration) (Connection, error) {
	attempts := [][]string{hostKeyAlgorithmSets[target.HostKeyType]}
	if target.HostKeyType == "" {
		attempts = [][]string{nil}
		for _, family := range hostKeyFamilyOrder {
			attempts = append(attempts, hostKeyAlgorithmSets[family])
		}
	}
	// A retry is allowed only (a) after our own HostKeyCallback rejected the
	// presented key (typed hostKeyMismatchError), or (b) for a failure that
	// happened before authentication started, once such a rejection was seen
	// (the server has no key of the restricted family). Anything after the
	// first authentication callback, including any server disconnect, ends
	// the process: credentials are never replayed.
	sawMismatch := false
	var lastErr error
	for _, algorithms := range attempts {
		connection, outcome, err := c.connectOnce(ctx, target, credential, timeout, algorithms)
		if err == nil {
			return connection, nil
		}
		lastErr = err
		switch {
		case outcome == outcomeHostKeyRejected:
			sawMismatch = true
		case outcome == outcomePreAuthFailure && sawMismatch:
		default:
			return nil, err
		}
	}
	if sawMismatch {
		return nil, NewError("ssh_host_key_mismatch", "SSH host key does not match the published fingerprint")
	}
	return nil, lastErr
}

type attemptOutcome int

const (
	outcomeOther attemptOutcome = iota
	outcomeHostKeyRejected
	outcomePreAuthFailure
)

func (c *SSHConnector) connectOnce(ctx context.Context, target Target, credential Credential, timeout time.Duration, algorithms []string) (Connection, attemptOutcome, error) {
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var authStarted, keyAccepted atomic.Bool
	auth, err := authMethod(credential, &authStarted)
	if err != nil {
		return nil, outcomeOther, err
	}
	dialHost, err := c.resolveDialHost(connectCtx, target.Host)
	if err != nil {
		return nil, outcomeOther, err
	}
	address := net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port))
	dialAddress := net.JoinHostPort(dialHost, fmt.Sprintf("%d", target.Port))
	netConn, err := c.dialer.DialContext(connectCtx, "tcp", dialAddress)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return nil, outcomeOther, NewError("ssh_connect_timeout", "SSH connection timed out")
		}
		return nil, outcomeOther, NewError("ssh_connection_failed", "SSH connection could not be established")
	}
	defer func() {
		if netConn != nil {
			_ = netConn.Close()
		}
	}()
	if timeout > 0 {
		_ = netConn.SetDeadline(time.Now().Add(timeout))
	}
	clientConfig := &gossh.ClientConfig{
		User: target.Username,
		Auth: []gossh.AuthMethod{auth},
		HostKeyCallback: func(hostname string, remote net.Addr, key gossh.PublicKey) error {
			if err := strictHostKey(target.HostKeyFingerprint)(hostname, remote, key); err != nil {
				return err
			}
			keyAccepted.Store(true)
			return nil
		},
		Timeout:           timeout,
		HostKeyAlgorithms: algorithms,
	}
	clientConn, channels, requests, err := gossh.NewClientConn(netConn, address, clientConfig)
	if err != nil {
		var hostKeyError *hostKeyMismatchError
		if errors.As(err, &hostKeyError) {
			return nil, outcomeHostKeyRejected, NewError("ssh_host_key_mismatch", "SSH host key does not match the published fingerprint")
		}
		if isTimeout(err) {
			return nil, outcomeOther, NewError("ssh_connect_timeout", "SSH handshake timed out")
		}
		outcome := outcomeOther
		// Once our callback accepted the key, no other family is ever tried.
		if !authStarted.Load() && !keyAccepted.Load() {
			outcome = outcomePreAuthFailure
		}
		return nil, outcome, NewError("ssh_authentication_failed", "SSH authentication or handshake failed")
	}
	_ = netConn.SetDeadline(time.Time{})
	netConn = nil
	return &sshConnection{client: gossh.NewClient(clientConn, channels, requests)}, outcomeOther, nil
}

func (c *SSHConnector) resolveDialHost(ctx context.Context, host string) (string, error) {
	if c.allowLoopback {
		return host, nil
	}
	if strings.EqualFold(host, "localhost") {
		return "", NewError("ssh_destination_forbidden", "SSH destination resolves to a reserved address")
	}
	if literal := net.ParseIP(host); literal != nil {
		if forbiddenIP(literal) {
			return "", NewError("ssh_destination_forbidden", "SSH destination resolves to a reserved address")
		}
		return literal.String(), nil
	}
	addresses, err := c.resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return "", NewError("ssh_connection_failed", "SSH destination cannot be resolved")
	}
	for _, address := range addresses {
		if forbiddenIP(address.IP) {
			return "", NewError("ssh_destination_forbidden", "SSH destination resolves to a reserved address")
		}
	}
	return addresses[0].IP.String(), nil
}

func forbiddenIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func authMethod(credential Credential, started *atomic.Bool) (gossh.AuthMethod, error) {
	if err := validateCredential(credential); err != nil {
		return nil, NewError("ssh_credential_unavailable", "%v", err)
	}
	switch credential.Method {
	case "password":
		return gossh.PasswordCallback(func() (string, error) {
			started.Store(true)
			return credential.Password, nil
		}), nil
	case "private_key":
		var signer gossh.Signer
		var err error
		if credential.Passphrase == "" {
			signer, err = gossh.ParsePrivateKey([]byte(credential.PrivateKey))
		} else {
			signer, err = gossh.ParsePrivateKeyWithPassphrase([]byte(credential.PrivateKey), []byte(credential.Passphrase))
		}
		if err != nil {
			return nil, NewError("ssh_credential_unavailable", "private key cannot be parsed")
		}
		return gossh.PublicKeysCallback(func() ([]gossh.Signer, error) {
			started.Store(true)
			return []gossh.Signer{signer}, nil
		}), nil
	default:
		return nil, NewError("ssh_credential_unavailable", "unsupported credential method")
	}
}

type hostKeyMismatchError struct{}

func (*hostKeyMismatchError) Error() string { return "host key mismatch" }

func strictHostKey(expected string) gossh.HostKeyCallback {
	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		if gossh.FingerprintSHA256(key) != expected {
			return &hostKeyMismatchError{}
		}
		return nil
	}
}

func isTimeout(err error) bool {
	var netError net.Error
	return errors.As(err, &netError) && netError.Timeout()
}

type sshConnection struct {
	client    *gossh.Client
	closeOnce sync.Once
}

func (c *sshConnection) Exec(ctx context.Context, command, cwd string, maxBytes int) (ExecResult, error) {
	started := time.Now()
	result := ExecResult{ExitCode: -1}
	session, err := c.client.NewSession()
	if err != nil {
		return result, NewError("ssh_session_lost", "SSH session channel could not be opened")
	}
	defer session.Close()
	stdout := &boundedWriter{limit: maxBytes}
	stderr := &boundedWriter{limit: maxBytes}
	session.Stdout = stdout
	session.Stderr = stderr
	remoteCommand := command
	if cwd != "" {
		remoteCommand = "cd -- " + shellQuote(cwd) + " && " + command
	}
	if err := session.Start(remoteCommand); err != nil {
		return result, NewError("ssh_execution_failed", "remote command could not be started")
	}
	wait := make(chan error, 1)
	go func() { wait <- session.Wait() }()
	select {
	case err := <-wait:
		result.Duration = time.Since(started).Milliseconds()
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.Truncated = stdout.Truncated() || stderr.Truncated()
		if err == nil {
			result.ExitCode = 0
			return result, nil
		}
		var exitError *gossh.ExitError
		if errors.As(err, &exitError) {
			result.ExitCode = exitError.ExitStatus()
			return result, nil
		}
		return result, NewError("ssh_session_lost", "SSH connection was lost during command execution")
	case <-ctx.Done():
		_ = session.Close()
		result.Duration = time.Since(started).Milliseconds()
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.Truncated = stdout.Truncated() || stderr.Truncated()
		result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		if result.TimedOut {
			return result, nil
		}
		return result, NewError("ssh_execution_cancelled", "remote command was cancelled")
	}
}

// SFTP opens the SFTP subsystem on the existing SSH connection.
func (c *sshConnection) SFTP() (*sftp.Client, error) { return sftp.NewClient(c.client) }

func (c *sshConnection) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.client.Close() })
	return err
}

type boundedWriter struct {
	mu        sync.Mutex
	value     strings.Builder
	limit     int
	truncated bool
}

func (w *boundedWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - w.value.Len()
	if remaining > 0 {
		written := len(payload)
		if written > remaining {
			written = remaining
		}
		_, _ = w.value.Write(payload[:written])
	}
	if len(payload) > remaining {
		w.truncated = true
	}
	return len(payload), nil
}

func (w *boundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.value.String()
}

func (w *boundedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
