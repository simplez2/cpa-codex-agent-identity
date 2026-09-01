package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/simplez2/cpa-codex-agent-identity/internal/cpa"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

const deleteBackupVersion = 1

type identityDeleteBackup struct {
	ID             string
	Directory      string
	CPASnapshots   []cpa.AuthFileSnapshot
	StoreFile      string
	StoreFileBytes []byte
}

type deleteBackupMetadata struct {
	Version        int                        `json:"version"`
	BackupID       string                     `json:"backup_id"`
	CreatedAt      time.Time                  `json:"created_at"`
	IdentityID     string                     `json:"identity_id"`
	Email          string                     `json:"email,omitempty"`
	CredentialKind string                     `json:"credential_kind,omitempty"`
	PlanType       string                     `json:"plan_type,omitempty"`
	StoreFile      string                     `json:"store_file"`
	StoreSHA256    string                     `json:"store_sha256"`
	AuthFiles      []deleteBackupAuthMetadata `json:"auth_files,omitempty"`
}

type deleteBackupAuthMetadata struct {
	Name       string `json:"name"`
	IdentityID string `json:"identity_id"`
	Disabled   bool   `json:"disabled"`
	SHA256     string `json:"sha256"`
}

func (s *Server) createIdentityDeleteBackup(ctx context.Context, identity *identitystore.Identity) (*identityDeleteBackup, string, error) {
	if identity == nil || strings.TrimSpace(identity.ID) == "" {
		return nil, "", errors.New("identity snapshot is required")
	}
	storeFile, storeRaw, err := s.store.SnapshotFile(identity.ID)
	if err != nil {
		return nil, "", fmt.Errorf("snapshot identity store: %w", err)
	}
	var authSnapshots []cpa.AuthFileSnapshot
	if s.channels != nil {
		authSnapshots, err = s.channels.SnapshotCredential(ctx, cpaCredentialFromStored(identity))
		if err != nil {
			return nil, "", fmt.Errorf("snapshot CPA auth files: %w", err)
		}
	}
	createdAt := time.Now().UTC()
	backupID := createdAt.Format("20060102T150405.000000000Z") + "-" + strings.TrimPrefix(identity.ID, "agent-")
	backupDirectory := filepath.Join(s.backupDir, backupID)
	if err = ensureOwnerOnlyDirectory(s.backupDir); err != nil {
		return nil, "", fmt.Errorf("prepare identity backup directory: %w", err)
	}
	if err = os.Mkdir(backupDirectory, 0o700); err != nil {
		return nil, "", fmt.Errorf("create identity backup directory: %w", err)
	}
	if err = os.Chmod(backupDirectory, 0o700); err != nil {
		_ = os.RemoveAll(backupDirectory)
		return nil, "", fmt.Errorf("secure identity backup directory: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.RemoveAll(backupDirectory)
		}
	}()

	storeDirectory := filepath.Join(backupDirectory, "store")
	if err = os.Mkdir(storeDirectory, 0o700); err != nil {
		return nil, "", fmt.Errorf("create store backup directory: %w", err)
	}
	if err = writeOwnerOnlyBackupFile(filepath.Join(storeDirectory, storeFile), storeRaw); err != nil {
		return nil, "", fmt.Errorf("write identity store backup: %w", err)
	}

	metadata := deleteBackupMetadata{
		Version:        deleteBackupVersion,
		BackupID:       backupID,
		CreatedAt:      createdAt,
		IdentityID:     identity.ID,
		Email:          maskedEmail(identity.Email),
		CredentialKind: identity.Kind,
		PlanType:       identity.PlanType,
		StoreFile:      filepath.Join("store", storeFile),
		StoreSHA256:    sha256Hex(storeRaw),
		AuthFiles:      make([]deleteBackupAuthMetadata, 0, len(authSnapshots)),
	}
	if len(authSnapshots) > 0 {
		cpaDirectory := filepath.Join(backupDirectory, "cpa")
		if err = os.Mkdir(cpaDirectory, 0o700); err != nil {
			return nil, "", fmt.Errorf("create CPA backup directory: %w", err)
		}
		for _, snapshot := range authSnapshots {
			name := filepath.Base(strings.TrimSpace(snapshot.Name))
			if name == "" || name != snapshot.Name || strings.Contains(name, "..") {
				return nil, "", errors.New("CPA auth-file backup name is invalid")
			}
			if err = writeOwnerOnlyBackupFile(filepath.Join(cpaDirectory, name), snapshot.Raw); err != nil {
				return nil, "", fmt.Errorf("write CPA auth-file backup: %w", err)
			}
			metadata.AuthFiles = append(metadata.AuthFiles, deleteBackupAuthMetadata{
				Name:       name,
				IdentityID: snapshot.IdentityID,
				Disabled:   snapshot.Disabled,
				SHA256:     sha256Hex(snapshot.Raw),
			})
		}
	}
	metadataRaw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, "", errors.New("encode identity delete backup metadata")
	}
	if err = writeOwnerOnlyBackupFile(filepath.Join(backupDirectory, "metadata.json"), metadataRaw); err != nil {
		return nil, "", fmt.Errorf("write identity delete backup metadata: %w", err)
	}
	if err = verifyBackupFile(filepath.Join(storeDirectory, storeFile), metadata.StoreSHA256); err != nil {
		return nil, "", fmt.Errorf("verify identity store backup: %w", err)
	}
	for _, file := range metadata.AuthFiles {
		if err = verifyBackupFile(filepath.Join(backupDirectory, "cpa", file.Name), file.SHA256); err != nil {
			return nil, "", fmt.Errorf("verify CPA auth-file backup: %w", err)
		}
	}
	removeOnError = false
	return &identityDeleteBackup{
		ID:             identity.ID,
		Directory:      backupDirectory,
		CPASnapshots:   authSnapshots,
		StoreFile:      storeFile,
		StoreFileBytes: storeRaw,
	}, backupID, nil
}

func writeOwnerOnlyBackupFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func verifyBackupFile(path, expectedSHA string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sha256Hex(data) != expectedSHA {
		return errors.New("backup hash mismatch")
	}
	return nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ensureOwnerOnlyDirectory(directory string) error {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return errors.New("identity backup directory is empty")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("identity backup directory must be a real directory")
	}
	return os.Chmod(directory, 0o700)
}
