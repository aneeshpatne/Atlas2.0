package main

import (
	"context"
	"encoding/json"
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
	font := flag.String("font", "/usr/java/lib/fonts/Helvetica_LT_65_Medium.ttf", "font path on the Kindle")
	fontSize := flag.Int("font-size", 0, "clock font size in pixels (0 = automatic)")
	storyFile := flag.String("story-file", "-", "story JSON file ('-' reads stdin)")
	flag.Parse()
	if flag.NArg() != 1 || (flag.Arg(0) != "clock" && flag.Arg(0) != "story") {
		fmt.Fprintf(os.Stderr, "usage: %s [options] <clock|story>\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		os.Exit(2)
	}
	mode := flag.Arg(0)

	var story kindle.Story
	if mode == "story" {
		reader := os.Stdin
		if *storyFile != "-" {
			file, err := os.Open(*storyFile)
			if err != nil {
				log.Fatalf("open story JSON: %v", err)
			}
			defer file.Close()
			reader = file
		}
		if err := json.NewDecoder(reader).Decode(&story); err != nil {
			log.Fatalf("decode story JSON: %v", err)
		}
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

	fmt.Printf("connected to %s; %s mode active (Ctrl-C to stop)\n", *address, mode)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	device := kindle.New(client)
	if mode == "clock" {
		if err := device.RunClock(ctx, kindle.ClockOptions{FontPath: *font, FontSize: *fontSize}); err != nil {
			log.Fatalf("clock mode: %v", err)
		}
		return
	}
	if err := device.RunStory(ctx, story, kindle.StoryOptions{FontPath: *font}); err != nil {
		log.Fatalf("story mode: %v", err)
	}
}
