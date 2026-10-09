package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// hotspotShotsDir returns the HOTSPOT_SHOTS environment variable.
func hotspotShotsDir() string { return os.Getenv("HOTSPOT_SHOTS") }

// writeShot encodes the tester's frame as a PNG.
func writeShot(t *testing.T, dir, name string, tt *ui.Tester) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
