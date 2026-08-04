package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	screenv1 "github.com/aneeshpatne/atlas/gen/screen/v1"
	"github.com/aneeshpatne/atlas/internal/config"
	"github.com/aneeshpatne/atlas/internal/dashboard"
	"github.com/aneeshpatne/atlas/internal/grpcserver"
	"github.com/aneeshpatne/atlas/internal/kindle"
	"github.com/aneeshpatne/atlas/internal/kindledisplay"
	"github.com/aneeshpatne/atlas/internal/news"
	"github.com/aneeshpatne/atlas/internal/newsworkflow"
	"github.com/aneeshpatne/atlas/internal/redis"
	"github.com/aneeshpatne/atlas/internal/scheduler"
	"github.com/aneeshpatne/atlas/internal/screennews"
	"github.com/aneeshpatne/atlas/internal/sshclient"
	"github.com/aneeshpatne/atlas/internal/supervisor"
	"google.golang.org/grpc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}
func run() error {
	cfg := config.Default()
	address := flag.String("kindle-address", "192.168.0.10:22", "Kindle SSH address")
	redisAddress := flag.String("redis", "localhost:6379", "Redis address")
	timezone := flag.String("timezone", cfg.Timezone, "schedule timezone")
	grpcAddress := flag.String("grpc", cfg.GRPCListenAddress, "gRPC listen address")
	alertDuration := flag.Duration("alert-duration", cfg.AlertDisplayDuration, "duration for each queued alert")
	newsRefresh := flag.Duration("news-refresh", cfg.NewsRefreshInterval, "wall-clock interval for news passes (must divide 1h; 15m → :00/:15/:30/:45)")
	maxNewsPerGenre := flag.Int("news-per-genre", 10, "maximum stories shown per genre in each pass")
	genreHold := flag.Duration("genre-hold", 10*time.Second, "how long to show each genre title")
	storyHold := flag.Duration("story-hold", 10*time.Second, "how long to show each story")
	fontPath := flag.String("font", "/usr/java/lib/fonts/Helvetica_LT_65_Medium.ttf", "font path on the Kindle for news stories")
	assetsDir := flag.String("assets", "assets/genres", "directory of genre backdrop images (india/mumbai/world/misc, etc.)")
	flag.Parse()
	cfg.Timezone = *timezone
	cfg.GRPCListenAddress = *grpcAddress
	cfg.AlertDisplayDuration = *alertDuration
	cfg.NewsRefreshInterval = *newsRefresh
	// Always clear the panel and zero brightness on SIGTERM/SIGINT and at the
	// scheduled stop hour (default 23:00 / 11pm).
	cfg.ShutdownDisplayOnServiceExit = true
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	// Resolve assets path so genre/story fallbacks work regardless of cwd.
	resolvedAssets, err := filepath.Abs(*assetsDir)
	if err != nil {
		return fmt.Errorf("assets directory: %w", err)
	}
	if st, err := os.Stat(resolvedAssets); err != nil || !st.IsDir() {
		return fmt.Errorf("assets directory %q must exist (genre backdrops)", resolvedAssets)
	}
	*assetsDir = resolvedAssets
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	root, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ssh, err := sshclient.NewSSHClient(sshclient.Config{Address: *address, Username: "root", PrivateKey: filepath.Join(home, ".ssh", "id_ed25519")})
	if err != nil {
		return fmt.Errorf("connect display: %w", err)
	}
	defer ssh.Close()
	redisClient, err := redis.New(root, redis.Config{Address: *redisAddress})
	if err != nil {
		return fmt.Errorf("connect news store: %w", err)
	}
	defer redisClient.Close()
	newsStore := news.NewStore(redisClient)
	repository := screennews.NewRedis(newsStore)
	kindleDevice := kindle.New(ssh)
	controller := kindledisplay.NewWithLocation(kindleDevice, loc)
	workflow := dashboard.New(newsStore, kindleDevice)
	worker := newsworkflow.New(workflow, controller, newsworkflow.Options{
		GenreHold:          *genreHold,
		StoryHold:          *storyHold,
		MaxStoriesPerGenre: *maxNewsPerGenre,
		FontPath:           *fontPath,
		AssetsDir:          *assetsDir,
	})
	sup := supervisor.New(root, cfg, controller, worker, controller, logger)
	sup.Start()
	now := time.Now().In(loc)
	desired := scheduler.IsInsideActiveWindow(now, cfg.StartHour, cfg.StartMinute, cfg.StopHour, cfg.StopMinute)
	reconcileCtx, cancel := context.WithTimeout(context.Background(), cfg.OperationTimeout)
	err = sup.Reconcile(reconcileCtx, desired)
	cancel()
	if err != nil {
		return fmt.Errorf("startup reconciliation: %w", err)
	}
	cronScheduler, err := scheduler.New(loc, cfg.StartHour, cfg.StartMinute, cfg.StopHour, cfg.StopMinute, cfg.NewsRefreshInterval, sup)
	if err != nil {
		return err
	}
	cronScheduler.Start()
	listener, err := net.Listen("tcp", cfg.GRPCListenAddress)
	if err != nil {
		return fmt.Errorf("listen gRPC: %w", err)
	}
	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcserver.UnaryInterceptor(logger, cfg.OperationTimeout)), grpc.MaxRecvMsgSize(1<<20), grpc.MaxSendMsgSize(1<<20))
	screenv1.RegisterNewsScreenServiceServer(grpcServer, grpcserver.New(sup, repository, cfg.AlertMessageMaxBytes, logger))
	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(listener) }()
	logger.Info("service_started", "grpc_address", listener.Addr().String(), "timezone", cfg.Timezone, "desired_on", desired)
	select {
	case <-root.Done():
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("gRPC serve: %w", err)
		}
	}
	logger.Info("service_stopping")
	cronCtx := cronScheduler.Stop()
	select {
	case <-cronCtx.Done():
	case <-time.After(cfg.OperationTimeout):
		logger.Warn("cron shutdown timed out")
	}
	grpcDone := make(chan struct{})
	go func() { grpcServer.GracefulStop(); close(grpcDone) }()
	select {
	case <-grpcDone:
	case <-time.After(cfg.GRPCShutdownTimeout):
		grpcServer.Stop()
		<-grpcDone
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.OperationTimeout+cfg.WorkerStopTimeout)
	defer cancel()
	if err := sup.Stop(shutdownCtx); err != nil {
		return fmt.Errorf("supervisor shutdown: %w", err)
	}
	logger.Info("service_stopped")
	return nil
}
