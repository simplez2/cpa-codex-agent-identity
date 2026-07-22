package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/simplez2/cpa-codex-agent-identity/internal/cpa"
	"github.com/simplez2/cpa-codex-agent-identity/internal/identity"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

type credentialImportResult struct {
	PublicIdentity *identitystore.PublicIdentity
	Credential     *identity.CredentialInfo
	ClientKey      string
	Duplicate      bool
}

type managementImportError struct {
	StatusCode int
	Code       string
	Message    string
}

func (s *Server) importTokenLocked(ctx context.Context, token string, skipExisting bool) (*credentialImportResult, *managementImportError) {
	credential, err := s.manager.Inspect(ctx, token)
	if err != nil {
		if identity.CredentialServiceUnavailable(err) {
			return nil, &managementImportError{StatusCode: http.StatusBadGateway, Code: "upstream_unavailable", Message: "credential validation service is unavailable"}
		}
		return nil, &managementImportError{StatusCode: http.StatusBadRequest, Code: "invalid", Message: "invalid codex access token"}
	}
	return s.commitInspectedTokenLocked(ctx, token, credential, skipExisting)
}

func (s *Server) commitInspectedTokenLocked(ctx context.Context, token string, credential *identity.CredentialInfo, skipExisting bool) (*credentialImportResult, *managementImportError) {
	identityID := identitystore.IdentityID(token)
	previous, hadPrevious := s.store.GetByID(identityID)
	if skipExisting && hadPrevious {
		public := publicIdentityFromStored(previous)
		return &credentialImportResult{PublicIdentity: &public, Credential: credential, Duplicate: true}, nil
	}
	publicIdentity, clientKey, err := s.store.ImportWithMetadata(token, storeMetadata(credential), time.Now())
	if err != nil {
		return nil, &managementImportError{StatusCode: http.StatusInternalServerError, Code: "store_failed", Message: "failed to store identity"}
	}
	if s.channels != nil {
		if err = s.channels.UpsertIdentity(ctx, cpaCredential(publicIdentity.ID, clientKey, credential)); err != nil {
			if hadPrevious {
				_ = s.store.Restore(previous)
			} else {
				_ = s.store.Delete(publicIdentity.ID)
			}
			return nil, &managementImportError{StatusCode: http.StatusBadGateway, Code: "sync_failed", Message: "failed to synchronize CPA Codex credential"}
		}
	}
	return &credentialImportResult{PublicIdentity: publicIdentity, Credential: credential, ClientKey: clientKey}, nil
}

func cpaCredential(identityID, clientKey string, credential *identity.CredentialInfo) cpa.Credential {
	if credential == nil {
		return cpa.Credential{IdentityID: identityID, ClientKey: clientKey}
	}
	return cpa.Credential{
		IdentityID: identityID,
		ClientKey:  clientKey,
		Kind:       string(credential.Kind),
		AccountID:  credential.AccountID,
		UserID:     credential.UserID,
		Email:      credential.Email,
		PlanType:   credential.PlanType,
		ExpiresAt:  credential.ExpiresAt,
		FedRAMP:    credential.FedRAMP,
	}
}

func storeMetadata(credential *identity.CredentialInfo) identitystore.CredentialMetadata {
	if credential == nil {
		return identitystore.CredentialMetadata{}
	}
	return identitystore.CredentialMetadata{
		Kind:      string(credential.Kind),
		Email:     credential.Email,
		PlanType:  credential.PlanType,
		ExpiresAt: credential.ExpiresAt,
		FedRAMP:   credential.FedRAMP,
	}
}

func publicIdentityFromStored(stored *identitystore.Identity) identitystore.PublicIdentity {
	if stored == nil {
		return identitystore.PublicIdentity{}
	}
	var expiresAt *time.Time
	if !stored.ExpiresAt.IsZero() {
		value := stored.ExpiresAt.UTC()
		expiresAt = &value
	}
	return identitystore.PublicIdentity{
		ID:        stored.ID,
		CreatedAt: stored.CreatedAt,
		Kind:      stored.Kind,
		Email:     stored.Email,
		PlanType:  stored.PlanType,
		ExpiresAt: expiresAt,
		FedRAMP:   stored.FedRAMP,
	}
}

func maskedEmail(value string) string {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	local := []rune(parts[0])
	if len(local) <= 2 {
		return string(local[:1]) + "***@" + parts[1]
	}
	return string(local[:1]) + "***" + string(local[len(local)-1:]) + "@" + parts[1]
}
