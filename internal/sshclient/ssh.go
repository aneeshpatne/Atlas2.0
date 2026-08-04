// Package sshclient provides a thin SSH client for running remote commands.
package sshclient

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

type SSHClient struct {
	client *ssh.Client
}

func NewSSHClient(config Config) (*SSHClient, error) {
	privateKey, err := os.ReadFile(config.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	sshConfig := &ssh.ClientConfig{
		User:            config.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", config.Address, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("connect to Kindle: %w", err)
	}
	return &SSHClient{client: client}, nil
}

func (c *SSHClient) Run(command string) (string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("create SSH session: %w", err)
	}
	defer session.Close()
	output, err := session.CombinedOutput(command)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return string(output), fmt.Errorf("run command: %w", err)
		}
		return string(output), fmt.Errorf("run command: %w: %s", err, message)
	}
	return string(output), nil
}

// Upload writes data to a file on the remote device through the existing SSH
// connection. The caller is responsible for supplying a trusted remote path.
func (c *SSHClient) Upload(path string, data []byte) error {
	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("create SSH upload session: %w", err)
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(data)
	output, err := session.CombinedOutput("cat > " + shellQuote(path))
	if err != nil {
		return fmt.Errorf("upload %s: %w: %s", path, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (c *SSHClient) Close() error {
	return c.client.Close()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
