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
	"strings"
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
	sshUser := flag.String("ssh-user", "root", "Kindle SSH username")
	sshKey := flag.String("ssh-key", "", "private key path (default ~/.ssh/id_ed25519)")
	sshKnownHosts := flag.String("ssh-known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	sshInsecureHostKey := flag.Bool("ssh-insecure-host-key", false, "disable SSH host-key verification (unsafe)")
	font := flag.String("font", "/usr/java/lib/fonts/Helvetica_LT_65_Medium.ttf", "font path on the Kindle")
	fontSize := flag.Int("font-size", 0, "clock font size in pixels (0 = automatic)")
	storyFile := flag.String("story-file", "-", "story JSON file ('-' reads stdin)")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address for news mode")
	redisPasswordFile := flag.String("redis-password-file", "", "file containing the Redis password")
	redisDB := flag.Int("redis-db", 0, "Redis database")
	redisPrefix := flag.String("redis-prefix", "", "Redis key prefix (empty preserves legacy keys)")
	newsQueueLimit := flag.Int64("news-queue-limit", 100, "maximum retained stories per genre")
	genre := flag.String("genre", "", "news genre filter (empty = all genres)")
	assetsDir := flag.String("assets", "assets/genres", "directory of genre backdrop images")
	allowPrivateImages := flag.Bool("image-allow-private", false, "allow story image URLs resolving to private/local networks")
	genreHold := flag.Duration("genre-hold", 10*time.Second, "how long to show each genre title screen")
	storyHold := flag.Duration("story-hold", 10*time.Second, "how long to show each story")
	flag.Parse()
	if flag.NArg() != 1 || (flag.Arg(0) != "clock" && flag.Arg(0) != "story" && flag.Arg(0) != "news") {
		fmt.Fprintf(os.Stderr, "usage: %s [options] <clock|story|news>\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		os.Exit(2)
	}
	mode := flag.Arg(0)
	if *newsQueueLimit <= 0 || *genreHold <= 0 || *storyHold <= 0 {
		log.Fatal("news-queue-limit, genre-hold, and story-hold must be positive")
	}

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

	if *sshKey == "" {
		*sshKey = filepath.Join(homeDir, ".ssh", "id_ed25519")
	}
	if *sshKnownHosts == "" {
		*sshKnownHosts = filepath.Join(homeDir, ".ssh", "known_hosts")
	}
	client, err := sshclient.NewSSHClient(sshclient.Config{
		Address:               *address,
		Username:              *sshUser,
		PrivateKey:            *sshKey,
		KnownHostsFile:        *sshKnownHosts,
		InsecureIgnoreHostKey: *sshInsecureHostKey,
		CommandTimeout:        30 * time.Second,
	})
	if err != nil {
		log.Fatalf("create SSH client: %v", err)
	}
	defer client.Close()

	fmt.Printf("connected to %s; %s mode active (Ctrl-C to stop)\n", *address, mode)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	device := kindle.New(client)
	cleanupDevice := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = device.ClearScreenContext(cleanupCtx)
		_ = device.SetBacklightContext(cleanupCtx, 0)
	}
	defer cleanupDevice()
	if mode == "clock" {
		if err := device.RunClock(ctx, kindle.ClockOptions{FontPath: *font, FontSize: *fontSize}); err != nil {
			cleanupDevice()
			log.Fatalf("clock mode: %v", err)
		}
		return
	}
	if mode == "story" {
		if err := device.RunStory(ctx, story, kindle.StoryOptions{FontPath: *font, AllowPrivateImage: *allowPrivateImages}); err != nil {
			cleanupDevice()
			log.Fatalf("story mode: %v", err)
		}
		return
	}

	redisPassword := ""
	if *redisPasswordFile != "" {
		info, statErr := os.Stat(*redisPasswordFile)
		if statErr != nil || info.Mode().Perm()&0o077 != 0 {
			log.Fatalf("Redis password file must exist and have no group/other permissions")
		}
		data, readErr := os.ReadFile(*redisPasswordFile)
		if readErr != nil {
			log.Fatalf("read Redis password: %v", readErr)
		}
		redisPassword = strings.TrimSpace(string(data))
	}
	redisClient, err := redis.New(ctx, redis.Config{Address: *redisAddr, Password: redisPassword, DB: *redisDB})
	if err != nil {
		cleanupDevice()
		log.Fatalf("connect redis: %v", err)
	}
	defer redisClient.Close()

	dash := dashboard.New(news.NewStoreWithOptions(redisClient, news.StoreOptions{KeyPrefix: *redisPrefix, MaxStories: *newsQueueLimit}), device)
	if err := dash.ShowNews(ctx, dashboard.Options{
		Genre:              *genre,
		FontPath:           *font,
		AssetsDir:          *assetsDir,
		GenreHold:          *genreHold,
		StoryHold:          *storyHold,
		AllowPrivateImages: *allowPrivateImages,
	}); err != nil {
		cleanupDevice()
		log.Fatalf("news mode: %v", err)
	}
}
