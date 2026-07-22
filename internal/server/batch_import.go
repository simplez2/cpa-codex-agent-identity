package server

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/simplez2/cpa-codex-agent-identity/internal/identity"
	identitystore "github.com/simplez2/cpa-codex-agent-identity/internal/store"
)

const batchValidationWorkers = 4

type batchImportItemResult struct {
	Index          int        `json:"index"`
	Label          string     `json:"label,omitempty"`
	IdentityID     string     `json:"identity_id,omitempty"`
	Status         string     `json:"status"`
	Code           string     `json:"code,omitempty"`
	Message        string     `json:"message,omitempty"`
	CredentialKind string     `json:"credential_kind,omitempty"`
	Email          string     `json:"email,omitempty"`
	PlanType       string     `json:"plan_type,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	FedRAMP        bool       `json:"fedramp,omitempty"`
	ChannelSynced  bool       `json:"channel_synced"`
	DuplicateOf    int        `json:"duplicate_of,omitempty"`
}

type batchImportSummary struct {
	Total               int `json:"total"`
	Ready               int `json:"ready"`
	Imported            int `json:"imported"`
	Duplicate           int `json:"duplicate"`
	Invalid             int `json:"invalid"`
	UpstreamUnavailable int `json:"upstream_unavailable"`
	Failed              int `json:"failed"`
	RolledBack          int `json:"rolled_back"`
	RollbackFailed      int `json:"rollback_failed"`
	Aborted             int `json:"aborted"`
}

type batchImportResponse struct {
	Status      string                  `json:"status"`
	Preview     bool                    `json:"preview"`
	Atomic      bool                    `json:"atomic"`
	Transaction string                  `json:"transaction"`
	Summary     batchImportSummary      `json:"summary"`
	Items       []batchImportItemResult `json:"items"`
}

type batchInspection struct {
	credential *identity.CredentialInfo
}

func (s *Server) handleBatchImport(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.authorizeManagement(request) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBatchImportBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid or oversized batch import body"})
		return
	}
	candidates, err := parseBatchCandidates(body)
	for index := range body {
		body[index] = 0
	}
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	preview := queryBool(request, "preview", false)
	atomic := queryBool(request, "atomic", true)
	if !preview {
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
	}
	response := s.processBatchImport(request.Context(), candidates, preview, atomic)
	for index := range candidates {
		candidates[index].Token = ""
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) processBatchImport(ctx context.Context, candidates []importCandidate, preview, atomic bool) batchImportResponse {
	items := make([]batchImportItemResult, len(candidates))
	inspections := make([]batchInspection, len(candidates))
	seen := make(map[string]int, len(candidates))
	jobs := make(chan int)
	for index, candidate := range candidates {
		identityID := identitystore.IdentityID(candidate.Token)
		items[index] = batchImportItemResult{Index: candidate.Index, Label: candidate.Label, IdentityID: identityID, Status: "pending"}
		if first, duplicate := seen[identityID]; duplicate {
			items[index].Status = "duplicate"
			items[index].Code = "duplicate_input"
			items[index].Message = "duplicate credential in this batch"
			items[index].DuplicateOf = first
			continue
		}
		seen[identityID] = candidate.Index
		if existing, ok := s.store.GetByID(identityID); ok {
			items[index].Status = "duplicate"
			items[index].Code = "already_imported"
			items[index].Message = "credential is already imported"
			populateBatchStoredMetadata(&items[index], existing)
		}
	}

	var workers sync.WaitGroup
	workerCount := batchValidationWorkers
	if workerCount > len(candidates) {
		workerCount = len(candidates)
	}
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				credential, err := s.manager.Inspect(ctx, candidates[index].Token)
				if err != nil {
					if identity.CredentialServiceUnavailable(err) {
						items[index].Status = "upstream_unavailable"
						items[index].Code = "upstream_unavailable"
						items[index].Message = "credential validation service is unavailable"
					} else {
						items[index].Status = "invalid"
						items[index].Code = "invalid"
						items[index].Message = "invalid codex access token"
					}
					continue
				}
				inspections[index].credential = credential
				items[index].Status = "ready"
				populateBatchCredentialMetadata(&items[index], credential)
			}
		}()
	}
	for index := range candidates {
		if items[index].Status == "pending" {
			jobs <- index
		}
	}
	close(jobs)
	workers.Wait()

	response := batchImportResponse{Status: "ok", Preview: preview, Atomic: atomic, Transaction: "preview", Items: items}
	if preview {
		response.Summary = summarizeBatchItems(items)
		return response
	}
	if atomic && batchHasBlockingValidation(items) {
		for index := range items {
			if items[index].Status == "ready" {
				items[index].Status = "aborted"
				items[index].Code = "atomic_validation_failed"
				items[index].Message = "not imported because another item failed validation"
			}
		}
		response.Status = "validation_failed"
		response.Transaction = "aborted"
		response.Items = items
		response.Summary = summarizeBatchItems(items)
		return response
	}

	imported := make([]int, 0, len(items))
	response.Transaction = "committed"
	for index := range items {
		if items[index].Status != "ready" {
			continue
		}
		result, importErr := s.commitInspectedTokenLocked(ctx, candidates[index].Token, inspections[index].credential, true)
		if importErr == nil && result != nil && result.Duplicate {
			items[index].Status = "duplicate"
			items[index].Code = "already_imported"
			items[index].Message = "credential is already imported"
			continue
		}
		if importErr != nil {
			items[index].Status = "failed"
			items[index].Code = importErr.Code
			items[index].Message = importErr.Message
			if atomic {
				rollbackOK := s.rollbackBatchImports(ctx, imported, items)
				for remaining := index + 1; remaining < len(items); remaining++ {
					if items[remaining].Status == "ready" {
						items[remaining].Status = "aborted"
						items[remaining].Code = "atomic_import_failed"
						items[remaining].Message = "not imported because the atomic transaction failed"
					}
				}
				if rollbackOK {
					response.Transaction = "rolled_back"
				} else {
					response.Transaction = "rollback_failed"
				}
				break
			}
			continue
		}
		items[index].Status = "imported"
		items[index].Code = "imported"
		items[index].Message = "credential imported and synchronized"
		items[index].ChannelSynced = s.channels != nil
		imported = append(imported, index)
	}
	response.Items = items
	response.Summary = summarizeBatchItems(items)
	if response.Summary.Failed > 0 || response.Summary.RollbackFailed > 0 {
		response.Status = "partial_failure"
	}
	return response
}

func (s *Server) rollbackBatchImports(ctx context.Context, imported []int, items []batchImportItemResult) bool {
	rollbackOK := true
	for position := len(imported) - 1; position >= 0; position-- {
		index := imported[position]
		identityID := items[index].IdentityID
		if s.channels != nil {
			if err := s.channels.RemoveIdentity(ctx, identityID); err != nil {
				items[index].Status = "rollback_failed"
				items[index].Code = "rollback_sync_failed"
				items[index].Message = "automatic rollback could not remove the CPA credential"
				rollbackOK = false
				continue
			}
		}
		if err := s.store.Delete(identityID); err != nil {
			items[index].Status = "rollback_failed"
			items[index].Code = "rollback_store_failed"
			items[index].Message = "automatic rollback could not remove the stored credential"
			rollbackOK = false
			continue
		}
		items[index].Status = "rolled_back"
		items[index].Code = "rolled_back"
		items[index].Message = "credential was rolled back because the atomic transaction failed"
		items[index].ChannelSynced = false
	}
	return rollbackOK
}

func queryBool(request *http.Request, name string, fallback bool) bool {
	value := strings.TrimSpace(request.URL.Query().Get(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func batchHasBlockingValidation(items []batchImportItemResult) bool {
	for _, item := range items {
		if item.Status == "invalid" || item.Status == "upstream_unavailable" {
			return true
		}
	}
	return false
}

func summarizeBatchItems(items []batchImportItemResult) batchImportSummary {
	summary := batchImportSummary{Total: len(items)}
	for _, item := range items {
		switch item.Status {
		case "ready":
			summary.Ready++
		case "imported":
			summary.Imported++
		case "duplicate":
			summary.Duplicate++
		case "invalid":
			summary.Invalid++
		case "upstream_unavailable":
			summary.UpstreamUnavailable++
		case "failed":
			summary.Failed++
		case "rolled_back":
			summary.RolledBack++
		case "rollback_failed":
			summary.RollbackFailed++
		case "aborted":
			summary.Aborted++
		}
	}
	return summary
}

func populateBatchCredentialMetadata(item *batchImportItemResult, credential *identity.CredentialInfo) {
	if item == nil || credential == nil {
		return
	}
	item.CredentialKind = string(credential.Kind)
	item.Email = maskedEmail(credential.Email)
	item.PlanType = credential.PlanType
	item.FedRAMP = credential.FedRAMP
	if !credential.ExpiresAt.IsZero() {
		value := credential.ExpiresAt.UTC()
		item.ExpiresAt = &value
	}
}

func populateBatchStoredMetadata(item *batchImportItemResult, stored *identitystore.Identity) {
	if item == nil || stored == nil {
		return
	}
	item.CredentialKind = stored.Kind
	item.Email = maskedEmail(stored.Email)
	item.PlanType = stored.PlanType
	item.FedRAMP = stored.FedRAMP
	if !stored.ExpiresAt.IsZero() {
		value := stored.ExpiresAt.UTC()
		item.ExpiresAt = &value
	}
}
