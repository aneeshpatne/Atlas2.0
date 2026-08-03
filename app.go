package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/aneeshpatne/atlas/internal/kindle"
	"github.com/aneeshpatne/atlas/internal/sshclient"
)

func main() {
	address := flag.String("address", "192.168.0.10:22", "Kindle SSH address")
	font := flag.String("font", "/mnt/us/fonts/InstrumentSerif-Regular.ttf", "font path on the Kindle")
	fontSize := flag.Int("font-size", 0, "clock font size in pixels (0 = automatic)")
	flag.Parse()
	if flag.NArg() != 1 || flag.Arg(0) != "clock" {
		fmt.Fprintf(os.Stderr, "usage: %s [options] clock\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		os.Exit(2)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("get home directory: %v", err)
	}

	client, err := sshclient.NewSSHClient(sshclient.Config{
		Address:    *address,
		Username:   "root",
		PrivateKey: filepath.Join(homeDir, ".ssh", "id_ed25519"),
	})
	if err != nil {
		log.Fatalf("create SSH client: %v", err)
	}
	defer client.Close()

	fmt.Printf("connected to %s; clock mode active (Ctrl-C to stop)\n", *address)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := kindle.New(client).RunClock(ctx, kindle.ClockOptions{FontPath: *font, FontSize: *fontSize}); err != nil {
		log.Fatalf("clock mode: %v", err)
	}
}
