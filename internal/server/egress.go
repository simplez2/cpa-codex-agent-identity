package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/simplez2/cpa-codex-agent-identity/internal/egress"
)

func (s *Server) withIdentityProxy(r *http.Request, id, override string) (*http.Request, error) {
	proxy := strings.TrimSpace(override)
	if proxy == "" && s.config.EnforceIdentityProxy {
		if s.channels == nil {
			return nil, errors.New("CPA routing unavailable")
		}
		var err error
		proxy, err = s.channels.ProxyForIdentity(r.Context(), id)
		if err != nil {
			return nil, err
		}
	}
	return r.WithContext(egress.WithProxy(r.Context(), proxy)), nil
}
