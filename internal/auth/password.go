package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id with the RFC 9106 recommended parameters (64 MiB, 3 passes, 4
// lanes). Hashes record their own parameters so these can be raised later
// and old hashes upgraded on the next login.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	saltLen      = 16
	keyLen       = 32

	MinPasswordLen = 12
	MaxPasswordLen = 256
	MinUsernameLen = 3
	MaxUsernameLen = 32
)

var b64 = base64.RawStdEncoding

func HashPassword(password string) string {
	salt := make([]byte, saltLen)
	rand.Read(salt)
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// VerifyPassword checks a password against a stored hash. stale reports a
// hash made with weaker parameters than the current ones.
func VerifyPassword(encoded, password string) (ok, stale bool, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, false, errors.New("unsupported hash format")
	}
	var version int
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, errors.New("unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, false, fmt.Errorf("hash parameters: %w", err)
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, false, fmt.Errorf("hash salt: %w", err)
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, false, fmt.Errorf("hash key: %w", err)
	}
	if memory == 0 || iterations == 0 || threads == 0 || len(key) == 0 {
		return false, false, errors.New("invalid hash parameters")
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(key)))
	ok = subtle.ConstantTimeCompare(got, key) == 1
	stale = memory < argonMemory || iterations < argonTime || len(key) < keyLen
	return ok, stale, nil
}

// CheckPassword applies the password policy: length (NIST 800-63B style,
// no composition rules), not built from the username, not trivially
// repetitive.
func CheckPassword(username, password string) error {
	if !utf8.ValidString(password) {
		return errors.New("password must be valid text")
	}
	if n := utf8.RuneCountInString(password); n < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(password) > MaxPasswordLen {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordLen)
	}
	lower := strings.ToLower(password)
	if username != "" && strings.Contains(lower, strings.ToLower(username)) {
		return errors.New("password must not contain the username")
	}
	if strings.Contains(lower, "tikscope") {
		return errors.New("password must not contain the application name")
	}
	distinct := map[rune]struct{}{}
	for _, r := range lower {
		distinct[r] = struct{}{}
	}
	if len(distinct) < 5 {
		return errors.New("password uses too few different characters")
	}
	return nil
}

// NormalizeUsername lowercases and validates a username.
func NormalizeUsername(username string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(username))
	if len(u) < MinUsernameLen || len(u) > MaxUsernameLen {
		return "", fmt.Errorf("username must be %d to %d characters", MinUsernameLen, MaxUsernameLen)
	}
	for _, r := range u {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' && r != '@' {
			return "", errors.New("username may contain letters, digits, '.', '_', '-' and '@'")
		}
	}
	return u, nil
}
