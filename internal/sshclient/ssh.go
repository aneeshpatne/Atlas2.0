// Package sshclient provides a thin SSH client for running remote commands.
package sshclient

import (
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

func (c *SSHClient) Close() error {
	return c.client.Close()
}
