package cpa

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// ProxyForIdentity fails closed when routing ownership cannot be established.
// It intentionally does not cache auth files: a proxy edit must affect new work.
func (m *Manager) ProxyForIdentity(ctx context.Context, id string) (string, error) {
	files, err := m.SnapshotIdentity(ctx, id)
	if err != nil || len(files) == 0 {
		return "", errors.New("CPA routing unavailable")
	}
	proxy := ""
	for i, file := range files {
		var fields struct {
			Proxy  string `json:"proxy_url"`
			Legacy string `json:"proxy-url"`
		}
		if json.Unmarshal(file.Raw, &fields) != nil {
			return "", errors.New("invalid CPA routing")
		}
		next := strings.TrimSpace(fields.Proxy)
		if next == "" {
			next = strings.TrimSpace(fields.Legacy)
		}
		if i > 0 && proxy != next {
			return "", errors.New("ambiguous CPA routing")
		}
		proxy = next
	}
	return proxy, nil
}
