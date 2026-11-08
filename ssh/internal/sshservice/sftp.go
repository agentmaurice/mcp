package sshservice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

const (
	// MaxUploadBytes is the fixed decoded size limit of one published file.
	MaxUploadBytes = 20 * 1024 * 1024
	// MaxListEntries bounds ssh_list_v1.
	MaxListEntries = 500
	maxPathBytes   = 1024
	tempPrefix     = ".mcp-ssh-upload-"
	tempSuffix     = ".tmp"
)

// posixRenameExtension is a variable so tests can simulate its absence.
var posixRenameExtension = "posix-rename@openssh.com"

var modePattern = regexp.MustCompile(`^0?[0-7]{3}$`)

// SFTPConnection is implemented by connections able to open an SFTP subsystem
// on the already authenticated and host-key-verified SSH connection.
type SFTPConnection interface {
	SFTP() (*sftp.Client, error)
}

type UploadResult struct {
	Status   string `json:"status"`
	TargetID string `json:"target"`
	Path     string `json:"path"`
	Bytes    int    `json:"bytes"`
	SHA256   string `json:"sha256"`
}

type RemoveResult struct {
	Status  string `json:"status"`
	Removed bool   `json:"removed"`
}

type ListEntry struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Mode     string `json:"mode"`
	Modified string `json:"modified"`
	Dir      bool   `json:"is_dir,omitempty"`
}

type ListResult struct {
	Entries   []ListEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// CleanPublishPath validates a path relative to the publication root. The raw
// value must be relative and contain no "..", NUL, backslash or empty segment.
// It returns the cleaned slash-separated path ("" for the root).
func CleanPublishPath(raw string, allowRoot bool) (string, error) {
	if raw == "" || raw == "." || raw == "./" {
		if allowRoot {
			return "", nil
		}
		return "", NewError("invalid_path", "path is required")
	}
	if len(raw) > maxPathBytes || strings.ContainsRune(raw, 0) || strings.ContainsRune(raw, '\\') {
		return "", NewError("invalid_path", "path is invalid")
	}
	if strings.HasPrefix(raw, "/") {
		return "", NewError("invalid_path", "path must be relative to the publication root")
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", NewError("invalid_path", "path must not contain '..'")
		}
	}
	cleaned := path.Clean(raw)
	if cleaned == "." {
		if allowRoot {
			return "", nil
		}
		return "", NewError("invalid_path", "path is required")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", NewError("invalid_path", "path must stay inside the publication root")
	}
	return cleaned, nil
}

func parseFileMode(value string) (os.FileMode, error) {
	if value == "" {
		return 0o644, nil
	}
	if !modePattern.MatchString(value) {
		return 0, NewError("invalid_arguments", "mode must be an octal permission such as 0644")
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, NewError("invalid_arguments", "mode must be an octal permission such as 0644")
	}
	mode := os.FileMode(parsed) & 0o777
	// The owner keeps read and write: the upload reads the file back to check its
	// sha256, and a later overwrite or removal must stay possible.
	if mode&0o600 != 0o600 {
		return 0, NewError("invalid_arguments", "mode must keep owner read and write (0600 or wider)")
	}
	return mode, nil
}

// publishTarget resolves a target that has a publication root.
func (s *Service) publishTarget(ctx context.Context, targetID string) (TargetSnapshot, Target, error) {
	snapshot, target, err := s.resolveTarget(ctx, targetID, false)
	if err != nil {
		return snapshot, target, err
	}
	if target.PublishRoot == "" {
		return snapshot, target, NewError("publish_not_enabled", "file publication is not enabled for this target")
	}
	return snapshot, target, nil
}

// withSFTP opens a one-shot connection with the regular credential, host-key
// and timeout path, runs fn and closes everything. The connection is torn down
// when ctx expires.
func (s *Service) withSFTP(ctx context.Context, snapshot TargetSnapshot, target Target, fn func(*sftp.Client, string) error) error {
	if !s.credentials.Available() {
		return NewError("credentials_provider_unavailable", "no credential provider is configured")
	}
	credential, err := s.credentials.Resolve(ctx, target.CredentialRef, Purpose{TargetID: target.ID, Revision: snapshot.Revision, Mode: s.config.Mode})
	if err != nil {
		return normalizeError(err, "ssh_credential_unavailable", "credential reference is unavailable")
	}
	connection, err := s.connector.Connect(ctx, target, credential, s.config.ConnectTimeout)
	if err != nil {
		return normalizeError(err, "ssh_connection_failed", "SSH connection failed")
	}
	defer connection.Close()
	transferable, ok := connection.(SFTPConnection)
	if !ok {
		return NewError("ssh_sftp_unavailable", "SFTP is not available on this connection")
	}
	opCtx, cancel := context.WithTimeout(ctx, s.config.MaxCommandTimeout)
	defer cancel()
	stop := context.AfterFunc(opCtx, func() { _ = connection.Close() })
	defer stop()
	client, err := transferable.SFTP()
	if err != nil {
		return NewError("ssh_sftp_unavailable", "SFTP subsystem could not be opened")
	}
	defer client.Close()
	root, err := client.RealPath(target.PublishRoot)
	if err != nil {
		return NewError("publish_root_unavailable", "publication root cannot be resolved")
	}
	root = path.Clean(root)
	info, err := client.Stat(root)
	if err != nil || !info.IsDir() {
		return NewError("publish_root_unavailable", "publication root is not a directory")
	}
	if err := fn(client, root); err != nil {
		var serviceError *Error
		if errors.As(err, &serviceError) {
			return serviceError
		}
		if opCtx.Err() != nil {
			return NewError("ssh_transfer_timeout", "SFTP transfer timed out")
		}
		return NewError("ssh_transfer_failed", "SFTP operation failed")
	}
	return nil
}

// walkParent checks every existing directory component below root: no symbolic
// link and no non-directory is tolerated. With create, missing directories are
// created. It returns the absolute directory and whether it exists.
func walkParent(client *sftp.Client, root, relDir string, create bool) (string, bool, error) {
	current := root
	if relDir == "" || relDir == "." {
		return current, true, nil
	}
	for _, segment := range strings.Split(relDir, "/") {
		next := path.Join(current, segment)
		info, err := client.Lstat(next)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return "", false, NewError("path_outside_root", "symbolic links are not allowed inside the publication root")
			}
			if !info.IsDir() {
				return "", false, NewError("not_a_directory", "a path component is not a directory")
			}
		case errors.Is(err, fs.ErrNotExist):
			if !create {
				return "", false, nil
			}
			if err := client.Mkdir(next); err != nil {
				return "", false, err
			}
			_ = client.Chmod(next, 0o755)
		default:
			return "", false, err
		}
		current = next
	}
	// Belt and braces: a server resolving links must agree with our walk.
	real, err := client.RealPath(current)
	if err != nil {
		return "", false, err
	}
	if !within(root, path.Clean(real)) {
		return "", false, NewError("path_outside_root", "path resolves outside the publication root")
	}
	return current, true, nil
}

func within(root, candidate string) bool {
	return candidate == root || strings.HasPrefix(candidate, strings.TrimSuffix(root, "/")+"/")
}

func randomHex(n int) string {
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		panic("crypto/rand unavailable")
	}
	return hex.EncodeToString(buffer)
}

// Upload writes content to a path below the publication root of a published
// target. The write is atomic: a temporary file in the destination directory
// is written, read back and verified, then renamed.
func (s *Service) Upload(ctx context.Context, targetID, relPath string, content []byte, overwrite bool, mode string) (UploadResult, error) {
	snapshot, target, err := s.publishTarget(ctx, targetID)
	if err != nil {
		return UploadResult{}, err
	}
	cleaned, err := CleanPublishPath(relPath, false)
	if err != nil {
		return UploadResult{}, err
	}
	if strings.HasPrefix(path.Base(cleaned), tempPrefix) {
		return UploadResult{}, NewError("invalid_path", "file name uses a reserved prefix")
	}
	if len(content) > MaxUploadBytes {
		return UploadResult{}, NewError("file_too_large", "file exceeds the %d byte limit", MaxUploadBytes)
	}
	perm, err := parseFileMode(mode)
	if err != nil {
		return UploadResult{}, err
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	result := UploadResult{Status: "ok", TargetID: target.ID, Path: cleaned, Bytes: len(content), SHA256: digest}
	err = s.withSFTP(ctx, snapshot, target, func(client *sftp.Client, root string) error {
		dir, _, err := walkParent(client, root, path.Dir(cleaned), true)
		if err != nil {
			return err
		}
		final := path.Join(dir, path.Base(cleaned))
		existing, statErr := client.Lstat(final)
		switch {
		case statErr == nil:
			if existing.Mode()&os.ModeSymlink != 0 || existing.IsDir() {
				return NewError("path_outside_root", "destination is a symbolic link or a directory")
			}
			if !overwrite {
				return NewError("file_exists", "destination already exists and overwrite is false")
			}
		case !errors.Is(statErr, fs.ErrNotExist):
			return statErr
		}
		temp := path.Join(dir, tempPrefix+randomHex(8)+tempSuffix)
		if err := writeVerified(client, temp, content, digest, perm); err != nil {
			_ = client.Remove(temp)
			return err
		}
		if err := replace(client, temp, final, overwrite); err != nil {
			_ = client.Remove(temp)
			return err
		}
		return nil
	})
	if err != nil {
		return UploadResult{}, err
	}
	return result, nil
}

func writeVerified(client *sftp.Client, temp string, content []byte, digest string, perm os.FileMode) error {
	file, err := client.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := client.Chmod(temp, perm); err != nil {
		return err
	}
	readback, err := client.Open(temp)
	if err != nil {
		return err
	}
	defer readback.Close()
	hasher := sha256.New()
	copied, err := io.Copy(hasher, readback)
	if err != nil {
		return err
	}
	if copied != int64(len(content)) || hex.EncodeToString(hasher.Sum(nil)) != digest {
		return NewError("checksum_mismatch", "remote content does not match the uploaded content")
	}
	return nil
}

func replace(client *sftp.Client, temp, final string, overwrite bool) error {
	if !overwrite {
		// SFTP v3 Rename refuses an existing destination on OpenSSH.
		if err := client.Rename(temp, final); err != nil {
			if _, statErr := client.Lstat(final); statErr == nil {
				return NewError("file_exists", "destination already exists and overwrite is false")
			}
			return err
		}
		return nil
	}
	if _, ok := client.HasExtension(posixRenameExtension); !ok {
		// Never remove then rename: that would expose a missing file.
		return NewError("atomic_replace_unavailable", "the server cannot replace a file atomically (posix-rename is missing); nothing was changed")
	}
	return client.PosixRename(temp, final)
}

// Remove deletes one regular file (or one symbolic link, never its target)
// below the publication root.
func (s *Service) Remove(ctx context.Context, targetID, relPath string) (RemoveResult, error) {
	snapshot, target, err := s.publishTarget(ctx, targetID)
	if err != nil {
		return RemoveResult{}, err
	}
	cleaned, err := CleanPublishPath(relPath, false)
	if err != nil {
		return RemoveResult{}, err
	}
	result := RemoveResult{Status: "ok"}
	err = s.withSFTP(ctx, snapshot, target, func(client *sftp.Client, root string) error {
		dir, exists, err := walkParent(client, root, path.Dir(cleaned), false)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		final := path.Join(dir, path.Base(cleaned))
		info, err := client.Lstat(final)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return NewError("not_a_file", "path is a directory; only files can be removed")
		}
		if err := client.Remove(final); err != nil {
			return err
		}
		result.Removed = true
		return nil
	})
	if err != nil {
		return RemoveResult{}, err
	}
	return result, nil
}

// List returns the entries of one directory below the publication root,
// non-recursive and bounded to MaxListEntries.
func (s *Service) List(ctx context.Context, targetID, relPath string) (ListResult, error) {
	snapshot, target, err := s.publishTarget(ctx, targetID)
	if err != nil {
		return ListResult{}, err
	}
	cleaned, err := CleanPublishPath(relPath, true)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Entries: []ListEntry{}}
	err = s.withSFTP(ctx, snapshot, target, func(client *sftp.Client, root string) error {
		dir, exists, err := walkParent(client, root, cleaned, false)
		if err != nil {
			return err
		}
		if !exists {
			return NewError("path_not_found", "directory does not exist")
		}
		infos, err := client.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
		for _, info := range infos {
			name := info.Name()
			if strings.HasPrefix(name, tempPrefix) && strings.HasSuffix(name, tempSuffix) {
				continue
			}
			if len(result.Entries) >= MaxListEntries {
				result.Truncated = true
				break
			}
			result.Entries = append(result.Entries, ListEntry{
				Path: path.Join(cleaned, name), Bytes: info.Size(), Mode: "0" + strconv.FormatUint(uint64(info.Mode().Perm()), 8),
				Modified: info.ModTime().UTC().Format(time.RFC3339), Dir: info.IsDir(),
			})
		}
		return nil
	})
	if err != nil {
		return ListResult{}, err
	}
	return result, nil
}
