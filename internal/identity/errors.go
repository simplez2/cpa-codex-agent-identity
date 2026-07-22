package identity

import "errors"

var (
	// ErrCredentialInvalid means the supplied Agent Identity or PAT cannot be trusted.
	ErrCredentialInvalid = errors.New("codex credential is invalid")
	// ErrCredentialServiceUnavailable means validation could not reach or trust the official metadata service.
	ErrCredentialServiceUnavailable = errors.New("codex credential service is unavailable")
)

// CredentialServiceUnavailable reports whether an import can be retried later without changing the token.
func CredentialServiceUnavailable(err error) bool {
	return errors.Is(err, ErrCredentialServiceUnavailable)
}
