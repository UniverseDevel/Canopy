// Package secrets keeps device passwords in the OS keychain (macOS Keychain,
// Windows Credential Manager, Secret Service on Linux), never in files.
package secrets

import (
	"errors"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/zalando/go-keyring"
)

const service = "canopy"

// key names the entry for a saved connection.
func key(preset string) string { return "connection/" + preset }

// Get returns the stored password; ok is false when there is none (or no
// keychain is available).
func Get(preset string) (string, bool) {
	pw, err := keyring.Get(service, key(preset))
	if err != nil {
		return "", false
	}
	return pw, true
}

// Set stores a password.
func Set(preset, password string) error {
	if err := keyring.Set(service, key(preset), password); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err).WithHint("the OS keychain is not available; use an environment variable instead")
	}
	return nil
}

// Delete removes a stored password (no error when there is none).
func Delete(preset string) error {
	if err := keyring.Delete(service, key(preset)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	return nil
}
