package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/aneeshpatne/atlas/internal/sshclient"
)

func main() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("get home directory: %v", err)
	}

	client, err := sshclient.NewSSHClient(sshclient.Config{
		Address:    "192.168.0.10:22",
		Username:   "root",
		PrivateKey: filepath.Join(homeDir, ".ssh", "id_ed25519"),
	})
	if err != nil {
		log.Fatalf("create SSH client: %v", err)
	}
	defer client.Close()

	fmt.Println("connected to 192.168.0.10")
}
