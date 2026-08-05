package sshclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHArgsUseStrictKnownHostsByDefault(t *testing.T) {
	c := &SSHClient{user: "root", host: "kindle.local", port: "22", privateKey: "/key", knownHostsFile: "/known"}
	joined := strings.Join(c.sshArgs("true"), " ")
	if !strings.Contains(joined, "StrictHostKeyChecking=yes") || !strings.Contains(joined, "UserKnownHostsFile=/known") {
		t.Fatalf("strict host-key options missing: %s", joined)
	}
	if strings.Contains(joined, "StrictHostKeyChecking=no") {
		t.Fatalf("insecure option unexpectedly enabled: %s", joined)
	}
}

func TestRunContextCancelsProcess(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ssh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &SSHClient{user: "root", host: "kindle.local", port: "22", privateKey: "/key", sshPath: script, insecureIgnoreHostKey: true, commandTimeout: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := c.RunContext(ctx, "true")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunContext error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took too long: %s", elapsed)
	}
}

func TestSplitHostPortRejectsOptionLikeHost(t *testing.T) {
	if _, _, err := splitHostPort("-oProxyCommand=bad"); err == nil {
		t.Fatal("expected invalid host to be rejected")
	}
}
