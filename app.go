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
	"time"

	"github.com/aneeshpatne/atlas/internal/dashboard"
	"github.com/aneeshpatne/atlas/internal/kindle"
	"github.com/aneeshpatne/atlas/internal/news"
	"github.com/aneeshpatne/atlas/internal/redis"
	"github.com/aneeshpatne/atlas/internal/sshclient"
)

func main() {
	address := flag.String("address", "192.168.0.10:22", "Kindle SSH address")
	font := flag.String("font", "/usr/java/lib/fonts/Helvetica_LT_65_Medium.ttf", "font path on the Kindle")
	fontSize := flag.Int("font-size", 0, "clock font size in pixels (0 = automatic)")
	storyFile := flag.String("story-file", "-", "story JSON file ('-' reads stdin)")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address for news mode")
	genre := flag.String("genre", "", "news genre filter (empty = all genres)")
	assetsDir := flag.String("assets", "assets/genres", "directory of genre backdrop images")
	genreHold := flag.Duration("genre-hold", 10*time.Second, "how long to show each genre title screen")
	storyHold := flag.Duration("story-hold", 10*time.Second, "how long to show each story")
	flag.Parse()
	if flag.NArg() != 1 || (flag.Arg(0) != "clock" && flag.Arg(0) != "story" && flag.Arg(0) != "news") {
		fmt.Fprintf(os.Stderr, "usage: %s [options] <clock|story|news>\n", filepath.Base(os.Args[0]))
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
	if mode == "story" {
		if err := device.RunStory(ctx, story, kindle.StoryOptions{FontPath: *font}); err != nil {
			log.Fatalf("story mode: %v", err)
		}
		return
	}

	redisClient, err := redis.New(ctx, redis.Config{Address: *redisAddr})
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer redisClient.Close()

	dash := dashboard.New(news.NewStore(redisClient), device)
	if err := dash.ShowNews(ctx, dashboard.Options{
		Genre:     *genre,
		FontPath:  *font,
		AssetsDir: *assetsDir,
		GenreHold: *genreHold,
		StoryHold: *storyHold,
	}); err != nil {
		log.Fatalf("news mode: %v", err)
	}
}
