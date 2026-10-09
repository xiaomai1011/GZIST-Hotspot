// Package settings keeps the hotspot configuration. The passkey lives in
// the OS credential store (Credential Manager on Windows, Keychain on
// macOS, Secret Service on Linux); when that is unavailable it falls back
// to a 0600 file next to the settings.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const (
	service = "io.github.xiaomai1011.gzist-hotspot"
	keyUser = "hotspot"
)

// Settings are saved as settings.json in the user data directory.
type Settings struct {
	Ssid string `json:"ssid"`
}

// Keyring is the subset of go-keyring the store uses; tests replace it.
type Keyring interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

type osKeyring struct{}

func (osKeyring) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (osKeyring) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (osKeyring) Delete(s, u string) error        { return keyring.Delete(s, u) }

// Store reads and writes settings in Dir.
type Store struct {
	Dir     string
	Keyring Keyring
}

// New returns a store backed by the OS credential store.
func New(dir string) *Store { return &Store{Dir: dir, Keyring: osKeyring{}} }

func (s *Store) settingsPath() string { return filepath.Join(s.Dir, "settings.json") }
func (s *Store) secretPath() string   { return filepath.Join(s.Dir, "passkey") }

// Load returns the saved settings, or defaults when none exist.
func (s *Store) Load() Settings {
	st := Settings{Ssid: "xiaomai-AP"}
	b, err := os.ReadFile(s.settingsPath())
	if err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

// Save writes the settings.
func (s *Store) Save(st Settings) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(s.settingsPath(), b, 0o600)
}

// SetPasskey stores the passkey. insecure reports that the credential
// store failed and the passkey was written to a plain file instead.
func (s *Store) SetPasskey(pass string) (insecure bool, err error) {
	if err := s.Keyring.Set(service, keyUser, pass); err == nil {
		os.Remove(s.secretPath())
		return false, nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return true, err
	}
	return true, os.WriteFile(s.secretPath(), []byte(pass), 0o600)
}

// Passkey returns the stored passkey. insecure reports that it came from
// the fallback file.
func (s *Store) Passkey() (pass string, insecure bool, err error) {
	if p, err := s.Keyring.Get(service, keyUser); err == nil {
		return p, false, nil
	}
	b, err := os.ReadFile(s.secretPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", true, err
	}
	return string(b), true, nil
}

// DeletePasskey forgets the stored passkey.
func (s *Store) DeletePasskey() {
	s.Keyring.Delete(service, keyUser)
	os.Remove(s.secretPath())
}

// Legacy is a hotspot_config.json from the PowerShell version.
type Legacy struct {
	Ssid    string
	Passkey string
}

// ParseLegacyConfig decodes the old config file format.
func ParseLegacyConfig(b []byte) (Legacy, error) {
	var j struct {
		Ssid    string `json:"Ssid"`
		Passkey string `json:"Passkey"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return Legacy{}, err
	}
	return Legacy{Ssid: j.Ssid, Passkey: j.Passkey}, nil
}
