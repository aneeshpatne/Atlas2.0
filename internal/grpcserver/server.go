package grpcserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
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
	Snapshot(context.Context) (supervisor.State, error)
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
	item, err := normalizeNews(item)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	id := operationID()
	if err := s.newsStore.Add(ctx, item); err != nil {
		return nil, status.Error(codes.Internal, "failed to store news item")
	}
	if err := s.supervisor.NotifyNewsChanged(ctx); err != nil {
		// Storage is the acceptance boundary. A scheduled pass will eventually
		// display the item even if the soft refresh notification cannot be queued.
		s.logger.Warn("news stored but immediate refresh was not requested", "operation_id", id, "error", err)
	}
	return &screenv1.CommandAck{Accepted: true, OperationId: id, State: "stored", CommandState: screenv1.CommandState_COMMAND_STATE_STORED}, nil
}
func (s *Server) AddAlert(ctx context.Context, req *screenv1.Alert) (*screenv1.CommandAck, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "alert is required")
	}
	color := strings.TrimSpace(req.Color)
	message := strings.TrimSpace(req.Message)
	if len(color) > 64 {
		return nil, status.Error(codes.InvalidArgument, "alert color exceeds 64 bytes")
	}
	if !utf8.ValidString(color) {
		return nil, status.Error(codes.InvalidArgument, "alert color must be valid UTF-8")
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
	var severity alert.Severity
	switch req.Severity {
	case screenv1.AlertSeverity_ALERT_SEVERITY_UNSPECIFIED, screenv1.AlertSeverity_ALERT_SEVERITY_INFO:
		severity = alert.SeverityInfo
	case screenv1.AlertSeverity_ALERT_SEVERITY_WARNING:
		severity = alert.SeverityWarning
	case screenv1.AlertSeverity_ALERT_SEVERITY_CRITICAL:
		severity = alert.SeverityCritical
	default:
		return nil, status.Error(codes.InvalidArgument, "alert severity is invalid")
	}
	result, err := s.supervisor.AddAlert(ctx, alert.Alert{OperationID: id, Color: color, Message: message, Severity: severity, ReceivedAt: time.Now()})
	if err != nil {
		return nil, mapError(err)
	}
	return &screenv1.CommandAck{Accepted: true, OperationId: id, State: result.State, CommandState: alertCommandState(result.State)}, nil
}

func alertCommandState(value string) screenv1.CommandState {
	if value == "displaying" {
		return screenv1.CommandState_COMMAND_STATE_DISPLAYING
	}
	if value == "queued" {
		return screenv1.CommandState_COMMAND_STATE_QUEUED
	}
	return screenv1.CommandState_COMMAND_STATE_UNSPECIFIED
}

func (s *Server) GetStatus(ctx context.Context, _ *screenv1.StatusRequest) (*screenv1.ServiceStatus, error) {
	snapshot, err := s.supervisor.Snapshot(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	return &screenv1.ServiceStatus{
		Lifecycle: lifecycleToProto(snapshot.Lifecycle), DesiredOn: snapshot.DesiredOn,
		DisplayRunning: snapshot.DisplayRunning, NewsRunning: snapshot.NewsRunning,
		AlertRunning: snapshot.AlertRunning, QueuedAlerts: int32(snapshot.QueuedAlerts),
		CurrentAlertOperationId: snapshot.CurrentAlertOperationID,
		LastStartedUnix:         unixOrZero(snapshot.LastStartedAt), LastStoppedUnix: unixOrZero(snapshot.LastStoppedAt),
		LastRefreshUnix: unixOrZero(snapshot.LastRefreshAt), LastError: sanitizeStatusError(snapshot.LastError),
	}, nil
}

func lifecycleToProto(value supervisor.LifecycleState) screenv1.LifecycleState {
	switch value {
	case supervisor.StateOff:
		return screenv1.LifecycleState_LIFECYCLE_STATE_OFF
	case supervisor.StateStarting:
		return screenv1.LifecycleState_LIFECYCLE_STATE_STARTING
	case supervisor.StateRunning:
		return screenv1.LifecycleState_LIFECYCLE_STATE_RUNNING
	case supervisor.StatePausing:
		return screenv1.LifecycleState_LIFECYCLE_STATE_PAUSING
	case supervisor.StateAlerting:
		return screenv1.LifecycleState_LIFECYCLE_STATE_ALERTING
	case supervisor.StateStopping:
		return screenv1.LifecycleState_LIFECYCLE_STATE_STOPPING
	case supervisor.StateFailed:
		return screenv1.LifecycleState_LIFECYCLE_STATE_FAILED
	default:
		return screenv1.LifecycleState_LIFECYCLE_STATE_UNSPECIFIED
	}
}

func sanitizeStatusError(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 512 {
		return string(runes[:512])
	}
	return value
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func validateNews(item *screenv1.NewsItem) error {
	_, err := normalizeNews(item)
	return err
}

func normalizeNews(item *screenv1.NewsItem) (*screenv1.NewsItem, error) {
	if item == nil {
		return nil, errors.New("news item is required")
	}
	out := &screenv1.NewsItem{
		StoryId: strings.TrimSpace(item.StoryId), EventId: strings.TrimSpace(item.EventId),
		Title: strings.TrimSpace(item.Title), Description: strings.TrimSpace(item.Description),
		Genre: strings.ToLower(strings.TrimSpace(item.Genre)),
	}
	for name, value := range map[string]string{"story ID": out.StoryId, "event ID": out.EventId, "title": out.Title, "description": out.Description, "genre": out.Genre} {
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("news %s must be valid UTF-8", name)
		}
	}
	if out.Title == "" {
		return nil, errors.New("news title is required")
	}
	if out.Genre == "" {
		return nil, errors.New("news genre is required")
	}
	if len(out.StoryId) > 128 || len(out.EventId) > 128 || len(out.Title) > 512 || len(out.Description) > 4096 || len(out.Genre) > 64 {
		return nil, errors.New("news item exceeds field size limits")
	}
	if len(item.Sources) > 10 {
		return nil, errors.New("news item exceeds 10 sources")
	}
	for i, source := range item.Sources {
		if source == nil {
			return nil, fmt.Errorf("news source %d is required", i)
		}
		normalized := &screenv1.Source{Url: strings.TrimSpace(source.Url), Domain: strings.TrimSpace(source.Domain), OgUrl: strings.TrimSpace(source.OgUrl)}
		if normalized.Url == "" && normalized.Domain == "" {
			return nil, fmt.Errorf("news source %d requires a URL or domain", i)
		}
		if !utf8.ValidString(normalized.Url) || !utf8.ValidString(normalized.Domain) || !utf8.ValidString(normalized.OgUrl) {
			return nil, fmt.Errorf("news source %d must be valid UTF-8", i)
		}
		if len(normalized.Url) > 2048 || len(normalized.OgUrl) > 2048 || len(normalized.Domain) > 253 {
			return nil, fmt.Errorf("news source %d exceeds field size limits", i)
		}
		for _, raw := range []string{normalized.Url, normalized.OgUrl} {
			if raw == "" {
				continue
			}
			parsed, err := url.ParseRequestURI(raw)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return nil, fmt.Errorf("news source %d contains an invalid HTTP(S) URL", i)
			}
		}
		out.Sources = append(out.Sources, normalized)
	}
	return out, nil
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
