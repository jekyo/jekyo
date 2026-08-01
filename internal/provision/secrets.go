package provision

import (
	"crypto/rand"
	"encoding/base32"

	"golang.org/x/crypto/bcrypt"
)

// NewPassword returns a random URL-safe password.
func NewPassword() (string, error) {
	raw := make([]byte, 15)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// BcryptHash hashes a password for htpasswd/wg-easy consumption.
func BcryptHash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}
