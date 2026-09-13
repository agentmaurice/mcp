package sshservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

var targetIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

type FileTargetStore struct {
	path string
}

func NewFileTargetStore(path string) *FileTargetStore {
	return &FileTargetStore{path: strings.TrimSpace(path)}
}

func (s *FileTargetStore) Snapshot(_ context.Context) (TargetSnapshot, error) {
	if s.path == "" {
		return TargetSnapshot{Revision: "empty", Targets: []Target{}}, nil
	}
	payload, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return TargetSnapshot{Revision: "empty", Targets: []Target{}}, nil
	}
	if err != nil {
		return TargetSnapshot{}, fmt.Errorf("read targets projection: %w", err)
	}
	var snapshot TargetSnapshot
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return TargetSnapshot{}, fmt.Errorf("decode targets projection: %w", err)
	}
	if err := validateSnapshot(&snapshot); err != nil {
		return TargetSnapshot{}, err
	}
	if snapshot.Revision == "" {
		digest := sha256.Sum256(payload)
		snapshot.Revision = hex.EncodeToString(digest[:8])
	}
	return snapshot, nil
}

func validateSnapshot(snapshot *TargetSnapshot) error {
	seen := make(map[string]struct{}, len(snapshot.Targets))
	for i := range snapshot.Targets {
		target := &snapshot.Targets[i]
		target.ID = strings.TrimSpace(target.ID)
		target.Label = strings.TrimSpace(target.Label)
		target.Host = strings.TrimSpace(target.Host)
		target.Username = strings.TrimSpace(target.Username)
		target.HostKeyFingerprint = strings.TrimSpace(target.HostKeyFingerprint)
		target.CredentialRef = strings.TrimSpace(target.CredentialRef)
		if !targetIDPattern.MatchString(target.ID) {
			return fmt.Errorf("invalid target_id %q", target.ID)
		}
		if _, ok := seen[target.ID]; ok {
			return fmt.Errorf("duplicate target_id %q", target.ID)
		}
		seen[target.ID] = struct{}{}
		if target.Label == "" || target.Host == "" || target.Username == "" {
			return fmt.Errorf("target %q requires label, host and username", target.ID)
		}
		if strings.ContainsAny(target.Host, "/@?#") {
			return fmt.Errorf("target %q host must be a hostname or IP literal", target.ID)
		}
		if target.Port < 1 || target.Port > 65535 {
			return fmt.Errorf("target %q port must be between 1 and 65535", target.ID)
		}
		if target.Published && !strings.HasPrefix(target.HostKeyFingerprint, "SHA256:") {
			return fmt.Errorf("target %q requires a SHA256 host key fingerprint before publication", target.ID)
		}
		if target.CredentialRef == "" {
			return fmt.Errorf("target %q requires credential_ref", target.ID)
		}
	}
	sort.Slice(snapshot.Targets, func(i, j int) bool { return snapshot.Targets[i].ID < snapshot.Targets[j].ID })
	return nil
}

type MemoryTargetStore struct {
	SnapshotValue TargetSnapshot
	Err           error
}

func (s *MemoryTargetStore) Snapshot(context.Context) (TargetSnapshot, error) {
	if s.Err != nil {
		return TargetSnapshot{}, s.Err
	}
	copyValue := s.SnapshotValue
	copyValue.Targets = append([]Target(nil), s.SnapshotValue.Targets...)
	return copyValue, nil
}
