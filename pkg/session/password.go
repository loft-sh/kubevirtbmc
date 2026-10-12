package session

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	pbkdf2Scheme     = "pbkdf2-sha256"
	pbkdf2Iterations = 100_000
	pbkdf2KeyLength  = 32
	pbkdf2SaltLength = 16
)

// HashPassword returns a salted PBKDF2-SHA256 hash in the form
// "pbkdf2-sha256$<iterations>$<salt>$<key>" (salt and key base64-encoded).
func HashPassword(password string) (string, error) {
	var salt [pbkdf2SaltLength]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	key, err := pbkdf2.Key(sha256.New, password, salt[:], pbkdf2Iterations, pbkdf2KeyLength)
	if err != nil {
		return "", fmt.Errorf("failed to derive key: %w", err)
	}

	return strings.Join([]string{
		pbkdf2Scheme,
		strconv.Itoa(pbkdf2Iterations),
		base64.RawStdEncoding.EncodeToString(salt[:]),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// VerifyPassword reports whether password matches a hash made by HashPassword.
// A malformed hash never matches.
func VerifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != pbkdf2Scheme {
		return false
	}

	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}

	return subtle.ConstantTimeCompare(got, want) == 1
}
