package sshclient

import "time"

type Config struct {
	Address               string
	Username              string
	PrivateKey            string
	KnownHostsFile        string
	InsecureIgnoreHostKey bool
	CommandTimeout        time.Duration
}
