package util

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// 24 bytes (192 bits) of entropy, base64 encoded
func RandomString32() (string, error) {

	b := make([]byte, 24)

	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	encoded := base64.URLEncoding.EncodeToString(b) // URLEncoding is with padding

	if len(encoded) < 32 {
		return "", errors.New("[RandomString32] Too short")
	}

	if len(encoded) > 32 {
		encoded = encoded[:32]
	}

	return encoded, nil
}
