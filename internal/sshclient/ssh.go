// Package sshclient provides a thin SSH client for running remote commands.
//
// On macOS, LaunchAgents cannot use raw Go TCP dials to LAN hosts without a
// Local Network TCC grant ("connect: no route to host"). System /usr/bin/ssh is
// allowed, so this client shells out to OpenSSH instead of golang.org/x/crypto/ssh.
package sshclient

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultSSH = "/usr/bin/ssh"

// SSHClient runs commands on a remote host via the system OpenSSH client.
type SSHClient struct {
	user                  string
	host                  string
	port                  string
	privateKey            string
	sshPath               string
	knownHostsFile        string
	insecureIgnoreHostKey bool
	commandTimeout        time.Duration
}

// NewSSHClient validates config and verifies the host is reachable over SSH.
func NewSSHClient(config Config) (*SSHClient, error) {
	if strings.TrimSpace(config.Address) == "" {
		return nil, fmt.Errorf("ssh address is required")
	}
	if strings.TrimSpace(config.Username) == "" {
		return nil, fmt.Errorf("ssh username is required")
	}
	if !validSSHUser.MatchString(config.Username) {
		return nil, fmt.Errorf("ssh username contains unsupported characters")
	}
	if strings.TrimSpace(config.PrivateKey) == "" {
		return nil, fmt.Errorf("ssh private key path is required")
	}
	keyInfo, err := os.Stat(config.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	if keyInfo.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("private key permissions are too open; remove group/other access")
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
		user:                  config.Username,
		host:                  host,
		port:                  port,
		privateKey:            config.PrivateKey,
		sshPath:               sshPath,
		knownHostsFile:        config.KnownHostsFile,
		insecureIgnoreHostKey: config.InsecureIgnoreHostKey,
		commandTimeout:        config.CommandTimeout,
	}
	if c.commandTimeout <= 0 {
		c.commandTimeout = 2 * time.Minute
	}
	if !c.insecureIgnoreHostKey && strings.TrimSpace(c.knownHostsFile) == "" {
		return nil, fmt.Errorf("known hosts file is required unless insecure host-key checking is enabled")
	}
	if !c.insecureIgnoreHostKey {
		if _, err := os.Stat(c.knownHostsFile); err != nil {
			return nil, fmt.Errorf("read known hosts: %w", err)
		}
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
		host, port = address, "22"
		if err := validateHostPort(host, port); err != nil {
			return "", "", err
		}
		return host, port, nil
	}
	h, p, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", fmt.Errorf("ssh address %q: %w", address, err)
	}
	if p == "" {
		p = "22"
	}
	if err := validateHostPort(h, p); err != nil {
		return "", "", err
	}
	return h, p, nil
}

var validSSHUser = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
var validSSHHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

func validateHostPort(host, port string) error {
	if net.ParseIP(host) == nil && !validSSHHost.MatchString(host) {
		return fmt.Errorf("ssh host contains unsupported characters")
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return fmt.Errorf("ssh port must be between 1 and 65535")
	}
	return nil
}

func (c *SSHClient) sshArgs(remoteCommand string) []string {
	args := []string{
		"-i", c.privateKey,
		"-p", c.port,
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
	if c.insecureIgnoreHostKey {
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "GlobalKnownHostsFile=/dev/null")
	} else {
		args = append(args, "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+c.knownHostsFile)
	}
	return append(args, fmt.Sprintf("%s@%s", c.user, c.host), remoteCommand)
}

// Run executes command on the remote host and returns combined stdout+stderr.
func (c *SSHClient) Run(command string) (string, error) {
	return c.RunContext(context.Background(), command)
}

// RunContext executes command and terminates the local SSH process when ctx or
// the configured command timeout expires.
func (c *SSHClient) RunContext(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.sshPath, c.sshArgs(command)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return string(output), fmt.Errorf("run command: %w", contextErr)
		}
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
	return c.UploadContext(context.Background(), path, data)
}

// UploadContext uploads data and honors both caller cancellation and the
// configured total command timeout.
func (c *SSHClient) UploadContext(ctx context.Context, path string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, c.commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.sshPath, c.sshArgs("cat > "+shellQuote(path))...)
	cmd.Stdin = bytes.NewReader(data)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("upload %s: %w", path, contextErr)
		}
		return fmt.Errorf("upload %s: %w: %s", path, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Close is a no-op; each Run/Upload opens its own OpenSSH process.
func (c *SSHClient) Close() error { return nil }

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
