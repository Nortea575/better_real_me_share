package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
)

const passwordIterations = 310_000

func randomSecret(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomID(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func secretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func derivePassword(password string, salt []byte, iterations int) []byte {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return nil
	}
	return key
}

func passwordMatches(password string, salt, expected []byte, iterations int) bool {
	if len(salt) < 16 || len(expected) != sha256.Size || iterations < 100_000 || iterations > 1_000_000 {
		return false
	}
	actual := derivePassword(password, salt, iterations)
	return hmac.Equal(actual, expected)
}
