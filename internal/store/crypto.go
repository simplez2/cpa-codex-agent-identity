package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

const encryptionKeySize = 32

type tokenCipher struct {
	aead cipher.AEAD
}

// ParseEncryptionKey accepts a 32-byte key encoded as hex or base64.
func ParseEncryptionKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("data encryption key is required")
	}
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == encryptionKeySize {
		return decoded, nil
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) == encryptionKeySize {
			return decoded, nil
		}
	}
	return nil, errors.New("data encryption key must be 32 bytes encoded as hex or base64")
}

func newTokenCipher(key []byte) (*tokenCipher, error) {
	if len(key) != encryptionKeySize {
		return nil, errors.New("data encryption key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("initialize data encryption cipher")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize data encryption mode")
	}
	return &tokenCipher{aead: aead}, nil
}

func (c *tokenCipher) encrypt(identityID, token string) (string, string, error) {
	if c == nil || c.aead == nil {
		return "", "", errors.New("data encryption cipher is unavailable")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", errors.New("generate data encryption nonce")
	}
	ciphertext := c.aead.Seal(nil, nonce, []byte(token), tokenAAD(identityID))
	return base64.RawStdEncoding.EncodeToString(nonce), base64.RawStdEncoding.EncodeToString(ciphertext), nil
}

func (c *tokenCipher) decrypt(identityID, encodedNonce, encodedCiphertext string) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("encrypted identity store requires a data encryption key")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(encodedNonce))
	if err != nil || len(nonce) != c.aead.NonceSize() {
		return "", errors.New("identity encryption nonce is invalid")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(encodedCiphertext))
	if err != nil || len(ciphertext) < c.aead.Overhead() {
		return "", errors.New("identity ciphertext is invalid")
	}
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, tokenAAD(identityID))
	if err != nil {
		return "", errors.New("identity ciphertext authentication failed")
	}
	return string(plaintext), nil
}

func tokenAAD(identityID string) []byte {
	// This legacy namespace is part of the encrypted on-disk format. Changing it
	// would make stores created before the public repository rename unreadable.
	return []byte("codex-access-token-sidecar:identity:v3:" + strings.TrimSpace(identityID))
}
