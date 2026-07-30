package sshclient

import "golang.org/x/crypto/ssh"

// ClientConfig is a type alias for the standard SSH client configuration.
type ClientConfig = ssh.ClientConfig

type Config struct {
	Address    string
	Username   string
	PrivateKey string
}
