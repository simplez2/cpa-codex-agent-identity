package cpa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
)

// UnmarshalJSON keeps the CPA auth-file boundary tolerant of the two shapes
// emitted by different CPA/keeper releases: auth_index can be either a JSON
// string or a JSON number, and some clients vary the key casing/style.
func (entry *authFileEntry) UnmarshalJSON(data []byte) error {
	if entry == nil {
		return errors.New("nil auth-file entry")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*entry = authFileEntry{}

	if raw, ok := lookupJSONField(fields, "name"); ok {
		name, err := decodeJSONString(raw)
		if err != nil {
			return fmt.Errorf("auth-file name: %w", err)
		}
		entry.Name = name
	}
	if raw, ok := lookupJSONField(fields, "auth_index", "authIndex", "AuthIndex"); ok {
		authIndex, err := decodeJSONStringOrNumber(raw)
		if err != nil {
			return fmt.Errorf("auth-file auth_index: %w", err)
		}
		entry.AuthIndex = authIndex
	}
	return nil
}

func lookupJSONField(fields map[string]json.RawMessage, aliases ...string) (json.RawMessage, bool) {
	for _, alias := range aliases {
		if raw, ok := fields[alias]; ok {
			return raw, true
		}
	}
	wanted := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		wanted[normalizeJSONFieldName(alias)] = struct{}{}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if _, ok := wanted[normalizeJSONFieldName(key)]; ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	return fields[keys[0]], true
}

func normalizeJSONFieldName(value string) string {
	var result strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character == '_' || character == '-' || unicode.IsSpace(character) {
			continue
		}
		result.WriteRune(character)
	}
	return result.String()
}

func decodeJSONString(raw json.RawMessage) (string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("must be a string")
	}
	return value, nil
}

func decodeJSONStringOrNumber(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		return decodeJSONString(trimmed)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", errors.New("must be a string or number")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", errors.New("must be a string or number")
	}
	if number, ok := value.(json.Number); ok {
		return number.String(), nil
	}
	return "", errors.New("must be a string or number")
}
