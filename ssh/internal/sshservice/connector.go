package sshservice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

type SSHConnector struct {
	dialer   net.Dialer
	resolver *net.Resolver
}

func NewSSHConnector() *SSHConnector { return &SSHConnector{resolver: net.DefaultResolver} }

func (c *SSHConnector) Connect(ctx context.Context, target Target, credential Credential, timeout time.Duration) (Connection, error) {
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	auth, err := authMethod(credential)
	if err != nil {
		return nil, err
	}
	dialHost, err := c.resolveDialHost(connectCtx, target.Host)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port))
	dialAddress := net.JoinHostPort(dialHost, fmt.Sprintf("%d", target.Port))
	netConn, err := c.dialer.DialContext(connectCtx, "tcp", dialAddress)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return nil, NewError("ssh_connect_timeout", "SSH connection timed out")
		}
		return nil, NewError("ssh_connection_failed", "SSH connection could not be established")
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
		User:            target.Username,
		Auth:            []gossh.AuthMethod{auth},
		HostKeyCallback: strictHostKey(target.HostKeyFingerprint),
		Timeout:         timeout,
	}
	clientConn, channels, requests, err := gossh.NewClientConn(netConn, address, clientConfig)
	if err != nil {
		var hostKeyError *hostKeyMismatchError
		if errors.As(err, &hostKeyError) {
			return nil, NewError("ssh_host_key_mismatch", "SSH host key does not match the published fingerprint")
		}
		if isTimeout(err) {
			return nil, NewError("ssh_connect_timeout", "SSH handshake timed out")
		}
		return nil, NewError("ssh_authentication_failed", "SSH authentication or handshake failed")
	}
	_ = netConn.SetDeadline(time.Time{})
	netConn = nil
	return &sshConnection{client: gossh.NewClient(clientConn, channels, requests)}, nil
}

func (c *SSHConnector) resolveDialHost(ctx context.Context, host string) (string, error) {
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

func authMethod(credential Credential) (gossh.AuthMethod, error) {
	if err := validateCredential(credential); err != nil {
		return nil, NewError("ssh_credential_unavailable", "%v", err)
	}
	switch credential.Method {
	case "password":
		return gossh.Password(credential.Password), nil
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
		return gossh.PublicKeys(signer), nil
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
