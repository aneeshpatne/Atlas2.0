// Package sshclient provides a thin SSH client for running remote commands.
//
// On macOS, LaunchAgents cannot use raw Go TCP dials to LAN hosts without a
// Local Network TCC grant ("connect: no route to host"). System /usr/bin/ssh is
// allowed, so this client shells out to OpenSSH instead of golang.org/x/crypto/ssh.
package sshclient

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const defaultSSH = "/usr/bin/ssh"

// SSHClient runs commands on a remote host via the system OpenSSH client.
type SSHClient struct {
	user       string
	host       string
	port       string
	privateKey string
	sshPath    string
}

// NewSSHClient validates config and verifies the host is reachable over SSH.
func NewSSHClient(config Config) (*SSHClient, error) {
	if strings.TrimSpace(config.Address) == "" {
		return nil, fmt.Errorf("ssh address is required")
	}
	if strings.TrimSpace(config.Username) == "" {
		return nil, fmt.Errorf("ssh username is required")
	}
	if strings.TrimSpace(config.PrivateKey) == "" {
		return nil, fmt.Errorf("ssh private key path is required")
	}
	if _, err := os.Stat(config.PrivateKey); err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}

	host, port, err := splitHostPort(config.Address)
	if err != nil {
		return nil, err
	}
	sshPath := defaultSSH
	if _, err := os.Stat(sshPath); err != nil {
		// Fall back to PATH (e.g. tests / non-macOS).
		if p, lookErr := exec.LookPath("ssh"); lookErr == nil {
			sshPath = p
		} else {
			return nil, fmt.Errorf("ssh client not found: %w", err)
		}
	}

	c := &SSHClient{
		user:       config.Username,
		host:       host,
		port:       port,
		privateKey: config.PrivateKey,
		sshPath:    sshPath,
	}
	// Fail fast at startup if the device is unreachable (matches previous dial behavior).
	if _, err := c.Run("true"); err != nil {
		return nil, fmt.Errorf("connect to Kindle: %w", err)
	}
	return c, nil
}

func splitHostPort(address string) (host, port string, err error) {
	// net.SplitHostPort requires brackets for IPv6; Kindle is IPv4 host:port.
	if strings.Count(address, ":") == 0 {
		return address, "22", nil
	}
	h, p, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", fmt.Errorf("ssh address %q: %w", address, err)
	}
	if p == "" {
		p = "22"
	}
	return h, p, nil
}

func (c *SSHClient) sshArgs(remoteCommand string) []string {
	return []string{
		"-i", c.privateKey,
		"-p", c.port,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		fmt.Sprintf("%s@%s", c.user, c.host),
		remoteCommand,
	}
}

// Run executes command on the remote host and returns combined stdout+stderr.
func (c *SSHClient) Run(command string) (string, error) {
	cmd := exec.Command(c.sshPath, c.sshArgs(command)...)
	// Avoid hanging forever if something stalls after connect.
	// ConnectTimeout covers dial; this caps total command time for safety.
	// Callers that need long operations can be adjusted later.
	timer := time.AfterFunc(2*time.Minute, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()

	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return string(output), fmt.Errorf("run command: %w", err)
		}
		return string(output), fmt.Errorf("run command: %w: %s", err, message)
	}
	return string(output), nil
}

// Upload writes data to a file on the remote device through SSH stdin.
// The caller is responsible for supplying a trusted remote path.
func (c *SSHClient) Upload(path string, data []byte) error {
	cmd := exec.Command(c.sshPath, c.sshArgs("cat > "+shellQuote(path))...)
	cmd.Stdin = bytes.NewReader(data)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("upload %s: %w: %s", path, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Close is a no-op; each Run/Upload opens its own OpenSSH process.
func (c *SSHClient) Close() error { return nil }

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
