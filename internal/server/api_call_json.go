package server

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

// UnmarshalJSON accepts the auth_index spellings emitted by CPA and keeper,
// including numeric JSON values. The original request body is still retained
// for native OAuth forwarding; this type only normalizes the sidecar lookup
// fields used by the quota bridge.
func (call *cpaAPICallRequest) UnmarshalJSON(data []byte) error {
	if call == nil {
		return errors.New("nil CPA api-call request")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*call = cpaAPICallRequest{}

	var err error
	if raw, ok := lookupAPICallJSONField(fields, "auth_index"); ok {
		call.AuthIndexSnake, err = decodeAPICallStringOrNumber(raw)
		if err != nil {
			return fmt.Errorf("auth_index: %w", err)
		}
	}
	if raw, ok := lookupAPICallJSONField(fields, "authIndex"); ok {
		call.AuthIndexCamel, err = decodeAPICallStringOrNumber(raw)
		if err != nil {
			return fmt.Errorf("authIndex: %w", err)
		}
	}
	if raw, ok := lookupAPICallJSONField(fields, "AuthIndex"); ok {
		call.AuthIndexPascal, err = decodeAPICallStringOrNumber(raw)
		if err != nil {
			return fmt.Errorf("AuthIndex: %w", err)
		}
	}
	if raw, ok := lookupAPICallJSONField(fields, "method"); ok {
		call.Method, err = decodeAPICallString(raw)
		if err != nil {
			return fmt.Errorf("method: %w", err)
		}
	}
	if raw, ok := lookupAPICallJSONField(fields, "url"); ok {
		call.URL, err = decodeAPICallString(raw)
		if err != nil {
			return fmt.Errorf("url: %w", err)
		}
	}
	if raw, ok := lookupAPICallJSONField(fields, "header"); ok && !isJSONNull(raw) {
		if err = json.Unmarshal(raw, &call.Header); err != nil {
			return errors.New("header must be an object")
		}
	}
	if raw, ok := lookupAPICallJSONField(fields, "data"); ok {
		call.Data, err = decodeAPICallString(raw)
		if err != nil {
			return fmt.Errorf("data: %w", err)
		}
	}
	return nil
}

func lookupAPICallJSONField(fields map[string]json.RawMessage, aliases ...string) (json.RawMessage, bool) {
	for _, alias := range aliases {
		if raw, ok := fields[alias]; ok {
			return raw, true
		}
	}
	wanted := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		wanted[normalizeAPICallJSONFieldName(alias)] = struct{}{}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if _, ok := wanted[normalizeAPICallJSONFieldName(key)]; ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	return fields[keys[0]], true
}

func normalizeAPICallJSONFieldName(value string) string {
	var result strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character == '_' || character == '-' || unicode.IsSpace(character) {
			continue
		}
		result.WriteRune(character)
	}
	return result.String()
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func decodeAPICallString(raw json.RawMessage) (string, error) {
	if isJSONNull(raw) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("must be a string")
	}
	return value, nil
}

func decodeAPICallStringOrNumber(raw json.RawMessage) (*string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		value, err := decodeAPICallString(trimmed)
		if err != nil {
			return nil, err
		}
		return &value, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("must be a string or number")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("must be a string or number")
	}
	if number, ok := value.(json.Number); ok {
		text := number.String()
		return &text, nil
	}
	return nil, errors.New("must be a string or number")
}

func containsSidecarCredentialHeader(headers map[string]string) bool {
	for name, value := range headers {
		if !strings.EqualFold(strings.TrimSpace(name), "authorization") {
			continue
		}
		token := bearerToken(value)
		if strings.HasPrefix(token, "cais_") {
			return true
		}
	}
	return false
}
