package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBackdropCaseInsensitiveAndMiscFallback(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name string, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("india.png", "india-bytes")
	mustWrite("World.JPG", "world-bytes")
	mustWrite("misc.png", "misc-bytes")

	data, path, err := loadBackdrop(dir, "INDIA")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "india-bytes" {
		t.Fatalf("data = %q, want india-bytes", data)
	}
	if filepath.Base(path) != "india.png" {
		t.Fatalf("path = %q, want india.png", path)
	}

	data, path, err = loadBackdrop(dir, "world")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world-bytes" || filepath.Base(path) != "World.JPG" {
		t.Fatalf("got data=%q path=%q", data, path)
	}

	data, path, err = loadBackdrop(dir, "Politics")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "misc-bytes" || filepath.Base(path) != "misc.png" {
		t.Fatalf("fallback got data=%q path=%q", data, path)
	}
}

func TestLoadBackdropMissingMisc(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "india.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadBackdrop(dir, "Unknown")
	if err == nil {
		t.Fatal("expected error when misc is missing")
	}
}
