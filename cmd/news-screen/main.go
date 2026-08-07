package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
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
	sshUser := flag.String("ssh-user", "root", "Kindle SSH username")
	sshKey := flag.String("ssh-key", "", "private key path (default ~/.ssh/id_ed25519)")
	sshKnownHosts := flag.String("ssh-known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
	sshInsecureHostKey := flag.Bool("ssh-insecure-host-key", false, "disable SSH host-key verification (unsafe)")
	redisAddress := flag.String("redis", "localhost:6379", "Redis address")
	redisPasswordFile := flag.String("redis-password-file", "", "file containing the Redis password")
	redisDB := flag.Int("redis-db", 0, "Redis database")
	redisPrefix := flag.String("redis-prefix", "", "Redis key prefix (empty preserves legacy keys)")
	newsQueueLimit := flag.Int64("news-queue-limit", 100, "maximum retained stories per genre")
	timezone := flag.String("timezone", cfg.Timezone, "schedule timezone")
	activeStart := flag.String("active-start", "07:00", "daily display start in HH:MM")
	activeStop := flag.String("active-stop", "23:00", "daily display stop in HH:MM")
	grpcAddress := flag.String("grpc", cfg.GRPCListenAddress, "gRPC listen address")
	grpcTokenFile := flag.String("grpc-token-file", "", "file containing bearer token for gRPC authentication")
	grpcTLSCert := flag.String("grpc-tls-cert", "", "TLS certificate for gRPC")
	grpcTLSKey := flag.String("grpc-tls-key", "", "TLS private key for gRPC")
	grpcClientCA := flag.String("grpc-client-ca", "", "CA bundle used to require and verify gRPC client certificates")
	alertDuration := flag.Duration("alert-duration", cfg.AlertDisplayDuration, "duration for each queued alert")
	alertQueueCapacity := flag.Int("alert-queue-capacity", cfg.AlertQueueCapacity, "maximum waiting alerts")
	allowOutsideAlerts := flag.Bool("allow-alerts-outside-window", cfg.AllowAlertsOutsideActiveWindow, "allow alerts to temporarily wake the display outside its active window")
	operationTimeout := flag.Duration("operation-timeout", cfg.OperationTimeout, "maximum duration of lifecycle and SSH operations")
	workerStopTimeout := flag.Duration("worker-stop-timeout", cfg.WorkerStopTimeout, "maximum wait for a display worker to stop")
	newsRefresh := flag.Duration("news-refresh", cfg.NewsRefreshInterval, "wall-clock interval for news passes (must divide 1h; 15m → :00/:15/:30/:45)")
	maxNewsPerGenre := flag.Int("news-per-genre", 10, "maximum stories shown per genre in each pass")
	genreHold := flag.Duration("genre-hold", 10*time.Second, "how long to show each genre title")
	storyHold := flag.Duration("story-hold", 10*time.Second, "how long to show each story")
	fontPath := flag.String("font", "/usr/java/lib/fonts/Helvetica_LT_65_Medium.ttf", "font path on the Kindle for news stories")
	assetsDir := flag.String("assets", "assets/genres", "directory of genre backdrop images (india/mumbai/world/misc, etc.)")
	allowPrivateImages := flag.Bool("image-allow-private", false, "allow story image URLs resolving to private/local networks")
	flag.Parse()
	cfg.Timezone = *timezone
	cfg.GRPCListenAddress = *grpcAddress
	cfg.AlertDisplayDuration = *alertDuration
	cfg.AlertQueueCapacity = *alertQueueCapacity
	cfg.AllowAlertsOutsideActiveWindow = *allowOutsideAlerts
	cfg.OperationTimeout = *operationTimeout
	cfg.WorkerStopTimeout = *workerStopTimeout
	cfg.NewsRefreshInterval = *newsRefresh
	startHour, startMinute, err := parseClock(*activeStart)
	if err != nil {
		return fmt.Errorf("active start: %w", err)
	}
	stopHour, stopMinute, err := parseClock(*activeStop)
	if err != nil {
		return fmt.Errorf("active stop: %w", err)
	}
	cfg.StartHour, cfg.StartMinute = startHour, startMinute
	cfg.StopHour, cfg.StopMinute = stopHour, stopMinute
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if *maxNewsPerGenre <= 0 || *genreHold <= 0 || *storyHold <= 0 {
		return fmt.Errorf("news-per-genre, genre-hold, and story-hold must be positive")
	}
	// Resolve assets path so genre/story fallbacks work regardless of cwd.
	resolvedAssets, err := filepath.Abs(*assetsDir)
	if err != nil {
		return fmt.Errorf("assets directory: %w", err)
	}
	if st, err := os.Stat(resolvedAssets); err != nil || !st.IsDir() {
		return fmt.Errorf("assets directory %q must exist (genre backdrops)", resolvedAssets)
	}
	if err := dashboard.ValidateBackdrops(resolvedAssets); err != nil {
		return fmt.Errorf("validate genre backdrops: %w", err)
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
	if *sshKey == "" {
		*sshKey = filepath.Join(home, ".ssh", "id_ed25519")
	}
	if *sshKnownHosts == "" {
		*sshKnownHosts = filepath.Join(home, ".ssh", "known_hosts")
	}
	ssh, err := sshclient.NewSSHClient(sshclient.Config{
		Address: *address, Username: *sshUser, PrivateKey: *sshKey,
		KnownHostsFile: *sshKnownHosts, InsecureIgnoreHostKey: *sshInsecureHostKey,
		CommandTimeout: cfg.OperationTimeout,
	})
	if err != nil {
		return fmt.Errorf("connect display: %w", err)
	}
	defer ssh.Close()
	if *newsQueueLimit <= 0 {
		return fmt.Errorf("news queue limit must be positive")
	}
	redisPassword := ""
	if *redisPasswordFile != "" {
		redisPassword, err = readPrivateTextFile(*redisPasswordFile, "Redis password")
		if err != nil {
			return err
		}
	}
	redisClient, err := redis.New(root, redis.Config{Address: *redisAddress, Password: redisPassword, DB: *redisDB})
	if err != nil {
		return fmt.Errorf("connect news store: %w", err)
	}
	defer redisClient.Close()
	newsStore := news.NewStoreWithOptions(redisClient, news.StoreOptions{KeyPrefix: *redisPrefix, MaxStories: *newsQueueLimit})
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
		AllowPrivateImages: *allowPrivateImages,
	})
	sup := supervisor.New(root, cfg, controller, worker, controller, logger)
	sup.Start()
	supervisorStopped := false
	defer func() {
		if supervisorStopped {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cfg.OperationTimeout+cfg.WorkerStopTimeout)
		defer cleanupCancel()
		if err := sup.Stop(cleanupCtx); err != nil {
			logger.Error("supervisor cleanup failed", "error", err)
		}
	}()
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
	defer listener.Close()
	unary := []grpc.UnaryServerInterceptor{grpcserver.UnaryInterceptor(logger, cfg.OperationTimeout)}
	if *grpcTokenFile != "" {
		token, err := readPrivateTextFile(*grpcTokenFile, "gRPC token")
		if err != nil {
			return err
		}
		if len(token) < 32 {
			return fmt.Errorf("gRPC bearer token must contain at least 32 characters")
		}
		unary = append(unary, grpcserver.BearerAuthInterceptor(token))
	}
	serverOptions := []grpc.ServerOption{grpc.ChainUnaryInterceptor(unary...), grpc.MaxRecvMsgSize(1 << 20), grpc.MaxSendMsgSize(1 << 20), grpc.MaxConcurrentStreams(32)}
	if (*grpcTLSCert == "") != (*grpcTLSKey == "") {
		return fmt.Errorf("both -grpc-tls-cert and -grpc-tls-key are required")
	}
	if *grpcClientCA != "" && *grpcTLSCert == "" {
		return fmt.Errorf("-grpc-client-ca requires -grpc-tls-cert and -grpc-tls-key")
	}
	if *grpcTLSCert != "" {
		keyInfo, keyErr := os.Stat(*grpcTLSKey)
		if keyErr != nil {
			return fmt.Errorf("stat gRPC TLS key: %w", keyErr)
		}
		if keyInfo.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("gRPC TLS key permissions are too open; remove group/other access")
		}
		var creds credentials.TransportCredentials
		if *grpcClientCA == "" {
			creds, err = credentials.NewServerTLSFromFile(*grpcTLSCert, *grpcTLSKey)
		} else {
			certificate, loadErr := tls.LoadX509KeyPair(*grpcTLSCert, *grpcTLSKey)
			if loadErr != nil {
				return fmt.Errorf("load gRPC TLS key pair: %w", loadErr)
			}
			caData, readErr := os.ReadFile(*grpcClientCA)
			if readErr != nil {
				return fmt.Errorf("read gRPC client CA: %w", readErr)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caData) {
				return fmt.Errorf("gRPC client CA contains no certificates")
			}
			creds = credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12})
		}
		if err != nil {
			return fmt.Errorf("load gRPC TLS credentials: %w", err)
		}
		serverOptions = append(serverOptions, grpc.Creds(creds))
	}
	if !isLoopbackAddress(cfg.GRPCListenAddress) {
		if *grpcTLSCert == "" {
			return fmt.Errorf("non-loopback gRPC listener requires TLS")
		}
		if *grpcTokenFile == "" && *grpcClientCA == "" {
			return fmt.Errorf("non-loopback gRPC listener requires -grpc-token-file or mutual TLS via -grpc-client-ca")
		}
	}
	grpcServer := grpc.NewServer(serverOptions...)
	screenv1.RegisterNewsScreenServiceServer(grpcServer, grpcserver.New(sup, repository, cfg.AlertMessageMaxBytes, logger))
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
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
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
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
	stopErr := sup.Stop(shutdownCtx)
	supervisorStopped = true
	if stopErr != nil {
		return fmt.Errorf("supervisor shutdown: %w", stopErr)
	}
	logger.Info("service_stopped")
	return nil
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseClock(value string) (int, int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, fmt.Errorf("must use HH:MM")
	}
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, 0, fmt.Errorf("must use HH:MM: %w", err)
	}
	return parsed.Hour(), parsed.Minute(), nil
}

func readPrivateTextFile(path, label string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", label, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s file permissions are too open; remove group/other access", label)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", label, err)
	}
	return strings.TrimSpace(string(data)), nil
}
