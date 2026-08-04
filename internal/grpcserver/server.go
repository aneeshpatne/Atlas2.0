package grpcserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	screenv1 "github.com/aneeshpatne/atlas/gen/screen/v1"
	"github.com/aneeshpatne/atlas/internal/alert"
	"github.com/aneeshpatne/atlas/internal/supervisor"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type NewsStore interface {
	Add(context.Context, *screenv1.NewsItem) error
}
type Lifecycle interface {
	AddAlert(context.Context, alert.Alert) (supervisor.AlertResult, error)
	NotifyNewsChanged(context.Context) error
}

type Server struct {
	screenv1.UnimplementedNewsScreenServiceServer
	supervisor    Lifecycle
	newsStore     NewsStore
	logger        *slog.Logger
	maxAlertBytes int
}

func New(l Lifecycle, store NewsStore, maxAlertBytes int, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{supervisor: l, newsStore: store, maxAlertBytes: maxAlertBytes, logger: logger}
}

func (s *Server) AddNews(ctx context.Context, item *screenv1.NewsItem) (*screenv1.CommandAck, error) {
	if err := validateNews(item); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	id := operationID()
	if err := s.newsStore.Add(ctx, item); err != nil {
		return nil, status.Error(codes.Internal, "failed to store news item")
	}
	if err := s.supervisor.NotifyNewsChanged(ctx); err != nil {
		return nil, mapError(err)
	}
	return &screenv1.CommandAck{Accepted: true, OperationId: id, State: "stored"}, nil
}
func (s *Server) AddAlert(ctx context.Context, req *screenv1.Alert) (*screenv1.CommandAck, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "alert is required")
	}
	color := strings.TrimSpace(req.Color)
	message := strings.TrimSpace(req.Message)
	if color == "" {
		return nil, status.Error(codes.InvalidArgument, "alert color is required")
	}
	if len(color) > 64 {
		return nil, status.Error(codes.InvalidArgument, "alert color exceeds 64 bytes")
	}
	if message == "" {
		return nil, status.Error(codes.InvalidArgument, "alert message is required")
	}
	if !utf8.ValidString(message) {
		return nil, status.Error(codes.InvalidArgument, "alert message must be valid UTF-8")
	}
	if len(message) > s.maxAlertBytes {
		return nil, status.Errorf(codes.InvalidArgument, "alert message exceeds %d bytes", s.maxAlertBytes)
	}
	id := operationID()
	result, err := s.supervisor.AddAlert(ctx, alert.Alert{OperationID: id, Color: color, Message: message, ReceivedAt: time.Now()})
	if err != nil {
		return nil, mapError(err)
	}
	return &screenv1.CommandAck{Accepted: true, OperationId: id, State: result.State}, nil
}

func validateNews(item *screenv1.NewsItem) error {
	if item == nil {
		return errors.New("news item is required")
	}
	if strings.TrimSpace(item.Title) == "" {
		return errors.New("news title is required")
	}
	if strings.TrimSpace(item.Genre) == "" {
		return errors.New("news genre is required")
	}
	for i, source := range item.Sources {
		if source == nil {
			return fmt.Errorf("news source %d is required", i)
		}
		if strings.TrimSpace(source.Url) == "" && strings.TrimSpace(source.Domain) == "" {
			return fmt.Errorf("news source %d requires a URL or domain", i)
		}
	}
	return nil
}
func mapError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled before acceptance")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline reached before acceptance")
	case errors.Is(err, supervisor.ErrShuttingDown):
		return status.Error(codes.Unavailable, "service is shutting down")
	case errors.Is(err, supervisor.ErrQueueFull):
		return status.Error(codes.ResourceExhausted, "alert queue is full")
	case errors.Is(err, supervisor.ErrOutsideActiveWindow):
		return status.Error(codes.FailedPrecondition, "alerts are not accepted outside the active window")
	default:
		return status.Error(codes.Internal, "lifecycle operation failed")
	}
}
func operationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
