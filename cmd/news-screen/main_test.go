package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseClock(t *testing.T) {
	hour, minute, err := parseClock("23:45")
	if err != nil || hour != 23 || minute != 45 {
		t.Fatalf("parseClock = %d:%d, %v", hour, minute, err)
	}
	if _, _, err := parseClock("7:00"); err == nil {
		t.Fatal("expected non-HH:MM value to fail")
	}
}

func TestLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:50050", "[::1]:50050", "localhost:50050"} {
		if !isLoopbackAddress(address) {
			t.Errorf("%q should be loopback", address)
		}
	}
	if isLoopbackAddress(":50050") || isLoopbackAddress("0.0.0.0:50050") {
		t.Fatal("wildcard listener treated as loopback")
	}
}

func TestReadPrivateTextFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("  secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readPrivateTextFile(path, "test secret")
	if err != nil || value != "secret-value" {
		t.Fatalf("readPrivateTextFile = %q, %v", value, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateTextFile(path, "test secret"); err == nil {
		t.Fatal("expected broad permissions to fail")
	}
}
