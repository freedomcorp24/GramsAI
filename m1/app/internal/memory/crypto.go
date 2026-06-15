// internal/memory/crypto.go
//
// Crypto primitives for encrypted chats + 3-tier summaries.
//
// Model (wrapped-key):
//   - Each user has ONE random DEK (data encryption key) that encrypts their
//     content. The DEK never derives from a secret and never changes.
//   - The DEK is stored WRAPPED: encrypted with a KEK (key-encryption-key) that
//     is derived from the user's password and (separately) their PIN via Argon2id.
//     Multiple wrapped copies let either secret unlock the same DEK without
//     re-encrypting content.
//   - Content (chat text, summary text) is encrypted with the DEK via AES-256-GCM.
//
// All functions here are pure (no Redis/DB/login). Unit-testable in isolation.
package memory

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	dekLen   = 32 // AES-256
	saltLen  = 16
	kekLen   = 32
	gcmNonce = 12

	// Argon2id params. Tuned for a login-time op (not per-message). Adjust upward
	// as hardware allows; these are sane 2025-era defaults.
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
)

var (
	ErrDecrypt   = errors.New("decryption failed (wrong key or corrupt data)")
	ErrBadLength = errors.New("bad input length")
)

// NewDEK returns a fresh random 32-byte data encryption key.
func NewDEK() ([]byte, error) {
	dek := make([]byte, dekLen)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, err
	}
	return dek, nil
}

// NewSalt returns a fresh random salt for KEK derivation.
func NewSalt() ([]byte, error) {
	s := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, s); err != nil {
		return nil, err
	}
	return s, nil
}

// DeriveKEK turns a secret (password or PIN) + salt into a 32-byte key via Argon2id.
func DeriveKEK(secret string, salt []byte) []byte {
	return argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, kekLen)
}

// gcmSeal encrypts plaintext with key (AES-256-GCM). Output = nonce || ciphertext.
func gcmSeal(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcmNonce)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plaintext, nil), nil
}

// gcmOpen reverses gcmSeal. Input = nonce || ciphertext.
func gcmOpen(key, blob []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcmNonce {
		return nil, ErrBadLength
	}
	nonce, ct := blob[:gcmNonce], blob[gcmNonce:]
	pt, err := g.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// WrapDEK encrypts the DEK with a KEK (derived from password or PIN).
// Returns the wrapped blob (nonce||ciphertext) to store in the DB.
func WrapDEK(kek, dek []byte) ([]byte, error) {
	if len(dek) != dekLen {
		return nil, ErrBadLength
	}
	return gcmSeal(kek, dek)
}

// UnwrapDEK recovers the DEK from a wrapped blob using the KEK.
// Wrong PIN/password -> ErrDecrypt (GCM auth fails).
func UnwrapDEK(kek, wrapped []byte) ([]byte, error) {
	dek, err := gcmOpen(kek, wrapped)
	if err != nil {
		return nil, err
	}
	if len(dek) != dekLen {
		return nil, ErrDecrypt
	}
	return dek, nil
}

// EncryptContent encrypts arbitrary content (chat text, summary) with the DEK.
// Returns ciphertext blob (nonce||ct). For the user_memory table this is what
// goes in content_enc; the nonce is embedded in the blob so content_nonce can
// hold an empty/zero value, OR we split it — see note in the writer (1d/1e).
func EncryptContent(dek, plaintext []byte) ([]byte, error) {
	return gcmSeal(dek, plaintext)
}

// DecryptContent reverses EncryptContent.
func DecryptContent(dek, blob []byte) ([]byte, error) {
	return gcmOpen(dek, blob)
}
