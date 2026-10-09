package settings

import (
	"os"
	"path/filepath"
	"testing"
)

type memKeyring struct {
	store map[string]string
	fail  bool
}

func newMemKeyring() *memKeyring      { return &memKeyring{store: map[string]string{}} }
func (k *memKeyring) Set(s, u, p string) error {
	if k.fail {
		return os.ErrPermission
	}
	k.store[s+"/"+u] = p
	return nil
}
func (k *memKeyring) Get(s, u string) (string, error) {
	if k.fail {
		return "", os.ErrPermission
	}
	p, ok := k.store[s+"/"+u]
	if !ok {
		return "", os.ErrNotExist
	}
	return p, nil
}
func (k *memKeyring) Delete(s, u string) error { delete(k.store, s+"/"+u); return nil }

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Keyring = newMemKeyring()

	if got := s.Load().Ssid; got != "xiaomai-AP" {
		t.Fatalf("default ssid %q", got)
	}
	if err := s.Save(Settings{Ssid: "my-ap"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Load().Ssid; got != "my-ap" {
		t.Fatalf("ssid %q", got)
	}
}

func TestPasskeyInKeyring(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Keyring = newMemKeyring()

	insecure, err := s.SetPasskey("secret")
	if err != nil || insecure {
		t.Fatalf("set: insecure=%v err=%v", insecure, err)
	}
	p, insecure, err := s.Passkey()
	if err != nil || insecure || p != "secret" {
		t.Fatalf("get: %q insecure=%v err=%v", p, insecure, err)
	}
	s.DeletePasskey()
	if p, _, _ := s.Passkey(); p != "" {
		t.Fatalf("after delete: %q", p)
	}
}

func TestPasskeyFallbackFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.Keyring = newMemKeyring()
	s.Keyring.(*memKeyring).fail = true

	insecure, err := s.SetPasskey("secret")
	if err != nil || !insecure {
		t.Fatalf("set: insecure=%v err=%v", insecure, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "passkey")); err != nil {
		t.Fatalf("fallback file missing: %v", err)
	}
	p, insecure, err := s.Passkey()
	if err != nil || !insecure || p != "secret" {
		t.Fatalf("get: %q insecure=%v err=%v", p, insecure, err)
	}
}

func TestPasskeyMissing(t *testing.T) {
	s := New(t.TempDir())
	s.Keyring = newMemKeyring()
	if p, insecure, err := s.Passkey(); p != "" || insecure || err != nil {
		t.Fatalf("expected empty, got %q insecure=%v err=%v", p, insecure, err)
	}
}

func TestParseLegacyConfig(t *testing.T) {
	lg, err := ParseLegacyConfig([]byte("{\"Ssid\": \"xiaomai-AP\", \"Passkey\": \"wifi12345\"}"))
	if err != nil {
		t.Fatal(err)
	}
	if lg.Ssid != "xiaomai-AP" || lg.Passkey != "wifi12345" {
		t.Fatalf("%+v", lg)
	}
	if _, err := ParseLegacyConfig([]byte("not json")); err == nil {
		t.Fatal("expected error")
	}
}
