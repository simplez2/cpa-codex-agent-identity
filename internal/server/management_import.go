package server

import (
	"context"
	"errors"
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

func (s *Server) importTokenLocked(ctx context.Context, token, accountID string, skipExisting bool) (*credentialImportResult, *managementImportError) {
	accountID = strings.TrimSpace(accountID)
	credential, err := s.manager.InspectForAccount(ctx, token, accountID)
	if err != nil {
		if identity.CredentialServiceUnavailable(err) {
			return nil, &managementImportError{StatusCode: http.StatusBadGateway, Code: "upstream_unavailable", Message: "credential validation service is unavailable"}
		}
		return nil, &managementImportError{StatusCode: http.StatusBadRequest, Code: "invalid", Message: "invalid codex access token"}
	}
	return s.commitInspectedTokenLocked(ctx, token, accountID, credential, skipExisting)
}

func (s *Server) commitInspectedTokenLocked(ctx context.Context, token, accountID string, credential *identity.CredentialInfo, skipExisting bool) (*credentialImportResult, *managementImportError) {
	accountID = strings.TrimSpace(accountID)
	accountScoped := accountID != ""
	if accountScoped && credential != nil {
		copyCredential := *credential
		copyCredential.AccountID = accountID
		credential = &copyCredential
	}
	previous, hadPrevious := s.store.LookupByTokenAndAccount(token, accountID)
	if skipExisting && hadPrevious {
		public := publicIdentityFromStored(previous)
		return &credentialImportResult{PublicIdentity: &public, Credential: credential, Duplicate: true}, nil
	}
	publicIdentity, clientKey, err := s.store.ImportWithMetadata(token, storeMetadata(credential, accountScoped), time.Now())
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
			return nil, cpaSynchronizationImportError(err)
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

func cpaCredentialFromStored(stored *identitystore.Identity) cpa.Credential {
	if stored == nil {
		return cpa.Credential{}
	}
	return cpa.Credential{
		IdentityID: stored.ID,
		ClientKey:  stored.ClientKey,
		Kind:       stored.Kind,
		AccountID:  stored.AccountID,
		Email:      stored.Email,
		PlanType:   stored.PlanType,
		ExpiresAt:  stored.ExpiresAt,
		FedRAMP:    stored.FedRAMP,
	}
}

func cpaCredentialFromPublic(public identitystore.PublicIdentity) cpa.Credential {
	credential := cpa.Credential{
		IdentityID: public.ID,
		Kind:       public.Kind,
		AccountID:  public.AccountID,
		Email:      public.Email,
		PlanType:   public.PlanType,
		FedRAMP:    public.FedRAMP,
	}
	if public.ExpiresAt != nil {
		credential.ExpiresAt = public.ExpiresAt.UTC()
	}
	return credential
}

func storeMetadata(credential *identity.CredentialInfo, accountScoped bool) identitystore.CredentialMetadata {
	if credential == nil {
		return identitystore.CredentialMetadata{}
	}
	return identitystore.CredentialMetadata{
		Kind:          string(credential.Kind),
		Email:         credential.Email,
		PlanType:      credential.PlanType,
		AccountID:     credential.AccountID,
		AccountScoped: accountScoped,
		ExpiresAt:     credential.ExpiresAt,
		FedRAMP:       credential.FedRAMP,
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
		ID:            stored.ID,
		CreatedAt:     stored.CreatedAt,
		Kind:          stored.Kind,
		Email:         stored.Email,
		PlanType:      stored.PlanType,
		AccountID:     stored.AccountID,
		AccountScoped: stored.AccountScoped,
		ExpiresAt:     expiresAt,
		FedRAMP:       stored.FedRAMP,
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

func cpaSynchronizationImportError(err error) *managementImportError {
	if errors.Is(err, cpa.ErrUnmanagedAuthFile) {
		return &managementImportError{
			StatusCode: http.StatusConflict,
			Code:       "cpa_auth_file_conflict",
			Message:    "CPA native OAuth already uses this auth-file name; upgrade Codex Agent Identity and re-import the credential",
		}
	}
	return &managementImportError{
		StatusCode: http.StatusBadGateway,
		Code:       "sync_failed",
		Message:    "failed to synchronize CPA Codex credential",
	}
}
