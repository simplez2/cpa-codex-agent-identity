package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxBatchImportBodyBytes = 4 << 20
	maxBatchImportItems     = 200
	maxBatchLabelRunes      = 128
	maxBatchAccountIDRunes  = 256
)

type importCandidate struct {
	Index     int
	Label     string
	Token     string
	AccountID string
}

func parseBatchCandidates(body []byte) ([]importCandidate, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errors.New("batch import body is empty")
	}
	var candidates []importCandidate
	if body[0] == '[' || body[0] == '{' || body[0] == '"' {
		if err := appendJSONCandidates(&candidates, body); err == nil {
			return finalizeCandidates(candidates)
		} else if body[0] != '{' {
			return nil, err
		}
	}
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	for _, rawLine := range strings.Split(normalized, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line[0] == '{' || line[0] == '[' || line[0] == '"' {
			if err := appendJSONCandidates(&candidates, []byte(line)); err != nil {
				return nil, errors.New("invalid JSONL import item")
			}
			continue
		}
		candidates = append(candidates, importCandidate{Token: line})
		if len(candidates) > maxBatchImportItems {
			return nil, fmt.Errorf("batch import supports at most %d items", maxBatchImportItems)
		}
	}
	return finalizeCandidates(candidates)
}

func appendJSONCandidates(target *[]importCandidate, raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return errors.New("invalid JSON import body")
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("import body contains multiple JSON values")
	}
	return appendCandidateValue(target, value, "", "")
}

func appendCandidateValue(target *[]importCandidate, value any, inheritedLabel, inheritedAccountID string) error {
	if len(*target) >= maxBatchImportItems {
		return fmt.Errorf("batch import supports at most %d items", maxBatchImportItems)
	}
	switch typed := value.(type) {
	case string:
		*target = append(*target, importCandidate{Token: strings.TrimSpace(typed), Label: normalizeBatchLabel(inheritedLabel), AccountID: normalizeBatchAccountID(inheritedAccountID)})
		return nil
	case []any:
		for _, item := range typed {
			if err := appendCandidateValue(target, item, inheritedLabel, inheritedAccountID); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		label := inheritedLabel
		accountID := inheritedAccountID
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "label", "name":
				text, ok := item.(string)
				if !ok {
					return errors.New("label field must be a string")
				}
				label = text
			case "account_id", "chatgpt_account_id", "team_id", "workspace_id":
				text, ok := item.(string)
				if !ok {
					return errors.New("account_id field must be a string")
				}
				accountID = text
			}
		}
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "items", "tokens", "credentials", "accounts":
				return appendCandidateValue(target, item, label, accountID)
			}
		}
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "codex_access_token", "agent_identity", "access_token", "token":
				text, ok := item.(string)
				if !ok {
					return errors.New("token field must be a string")
				}
				*target = append(*target, importCandidate{Token: strings.TrimSpace(text), Label: normalizeBatchLabel(label), AccountID: normalizeBatchAccountID(accountID)})
				return nil
			}
		}
		return errors.New("JSON import item omitted a token field")
	default:
		return errors.New("unsupported JSON import item")
	}
}

func finalizeCandidates(candidates []importCandidate) ([]importCandidate, error) {
	if len(candidates) == 0 {
		return nil, errors.New("batch import did not contain any credentials")
	}
	if len(candidates) > maxBatchImportItems {
		return nil, fmt.Errorf("batch import supports at most %d items", maxBatchImportItems)
	}
	for index := range candidates {
		candidates[index].Index = index + 1
		candidates[index].Token = strings.TrimSpace(candidates[index].Token)
		candidates[index].AccountID = normalizeBatchAccountID(candidates[index].AccountID)
		if candidates[index].Token == "" {
			return nil, fmt.Errorf("batch import item %d is empty", index+1)
		}
		if !utf8.ValidString(candidates[index].Token) {
			return nil, fmt.Errorf("batch import item %d is not valid UTF-8", index+1)
		}
		if !utf8.ValidString(candidates[index].AccountID) {
			return nil, fmt.Errorf("batch import item %d account_id is not valid UTF-8", index+1)
		}
		if len([]rune(candidates[index].AccountID)) > maxBatchAccountIDRunes {
			return nil, fmt.Errorf("batch import item %d account_id is too long", index+1)
		}
	}
	return candidates, nil
}

func normalizeBatchLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxBatchLabelRunes {
		runes = runes[:maxBatchLabelRunes]
	}
	return string(runes)
}

func normalizeBatchAccountID(value string) string {
	return strings.TrimSpace(value)
}
