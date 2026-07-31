package sshclient

import (
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
)

type SSHClient struct {
	client *ssh.Client
}

func NewSSHClient(config Config) (*SSHClient, error){
	privateKey, err := os.ReadFile(config.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
}