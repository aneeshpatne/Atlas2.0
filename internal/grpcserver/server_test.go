package grpcserver

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	screenv1 "github.com/aneeshpatne/atlas/gen/screen/v1"
	"github.com/aneeshpatne/atlas/internal/alert"
	"github.com/aneeshpatne/atlas/internal/supervisor"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type testStore struct {
	mu    sync.Mutex
	items []*screenv1.NewsItem
	err   error
}

func (s *testStore) Add(_ context.Context, item *screenv1.NewsItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
	return s.err
}

type testLifecycle struct {
	mu      sync.Mutex
	alerts  []alert.Alert
	states  []string
	err     error
	changed int
}

func (l *testLifecycle) AddAlert(_ context.Context, a alert.Alert) (supervisor.AlertResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return supervisor.AlertResult{}, l.err
	}
	state := "displaying"
	if len(l.alerts) > 0 {
		state = "queued"
	}
	l.alerts = append(l.alerts, a)
	return supervisor.AlertResult{OperationID: a.OperationID, State: state}, nil
}
func (l *testLifecycle) NotifyNewsChanged(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.changed++
	return l.err
}
func (l *testLifecycle) Snapshot(context.Context) (supervisor.State, error) {
	return supervisor.State{Lifecycle: supervisor.StateRunning, DesiredOn: true}, l.err
}
func newClient(t *testing.T, l *testLifecycle, s *testStore) (screenv1.NewsScreenServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	screenv1.RegisterNewsScreenServiceServer(server, New(l, s, 1000, nil))
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return screenv1.NewNewsScreenServiceClient(conn), func() { conn.Close(); server.Stop() }
}
func TestGRPCAddNewsAndQueuedAlerts(t *testing.T) {
	l := &testLifecycle{}
	store := &testStore{}
	client, closeFn := newClient(t, l, store)
	defer closeFn()
	ctx := context.Background()
	ack, err := client.AddNews(ctx, &screenv1.NewsItem{Title: " Title ", Genre: " India "})
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.State != "stored" || ack.CommandState != screenv1.CommandState_COMMAND_STATE_STORED || ack.OperationId == "" {
		t.Fatalf("bad ack %+v", ack)
	}
	store.mu.Lock()
	if got := store.items[0]; got.Title != "Title" || got.Genre != "india" {
		t.Fatalf("news was not normalized: %+v", got)
	}
	store.mu.Unlock()
	first, err := client.AddAlert(ctx, &screenv1.Alert{Color: " red ", Message: " first "})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.AddAlert(ctx, &screenv1.Alert{Color: "blue", Message: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "displaying" || second.State != "queued" {
		t.Fatalf("states %s %s", first.State, second.State)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.alerts[0].Message != "first" || l.alerts[0].Color != "red" {
		t.Fatalf("alert was not trimmed: %+v", l.alerts[0])
	}
}

func TestGRPCGetStatus(t *testing.T) {
	l := &testLifecycle{}
	client, closeFn := newClient(t, l, &testStore{})
	defer closeFn()
	got, err := client.GetStatus(context.Background(), &screenv1.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != screenv1.LifecycleState_LIFECYCLE_STATE_RUNNING || !got.DesiredOn {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestAddNewsRemainsAcceptedWhenRefreshSignalFails(t *testing.T) {
	l := &testLifecycle{err: errors.New("supervisor busy")}
	store := &testStore{}
	client, closeFn := newClient(t, l, store)
	defer closeFn()
	ack, err := client.AddNews(context.Background(), &screenv1.NewsItem{Title: "Stored", Genre: "world"})
	if err != nil || !ack.Accepted || ack.CommandState != screenv1.CommandState_COMMAND_STATE_STORED {
		t.Fatalf("AddNews = %+v, %v", ack, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) != 1 {
		t.Fatalf("stored items = %d, want 1", len(store.items))
	}
}

func TestBearerTokenValidation(t *testing.T) {
	if !validBearerToken("Bearer secret", "secret") {
		t.Fatal("valid token rejected")
	}
	for _, value := range []string{"secret", "bearer secret", "Bearer wrong", "Bearer secret extra"} {
		if validBearerToken(value, "secret") {
			t.Fatalf("invalid token accepted: %q", value)
		}
	}
}
func TestGRPCValidationAndErrorMapping(t *testing.T) {
	l := &testLifecycle{}
	client, closeFn := newClient(t, l, &testStore{})
	defer closeFn()
	tests := []struct {
		name string
		call func() error
		code codes.Code
	}{{"nil news", func() error { _, e := client.AddNews(context.Background(), nil); return e }, codes.InvalidArgument}, {"empty title", func() error { _, e := client.AddNews(context.Background(), &screenv1.NewsItem{Genre: "x"}); return e }, codes.InvalidArgument}, {"bad severity", func() error {
		_, e := client.AddAlert(context.Background(), &screenv1.Alert{Message: "x", Severity: screenv1.AlertSeverity(99)})
		return e
	}, codes.InvalidArgument}, {"empty message", func() error { _, e := client.AddAlert(context.Background(), &screenv1.Alert{Color: "red"}); return e }, codes.InvalidArgument}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status.Code(tt.call()); got != tt.code {
				t.Fatalf("got %s want %s", got, tt.code)
			}
		})
	}
	for _, tt := range []struct {
		err  error
		code codes.Code
	}{{supervisor.ErrQueueFull, codes.ResourceExhausted}, {supervisor.ErrOutsideActiveWindow, codes.FailedPrecondition}, {supervisor.ErrShuttingDown, codes.Unavailable}, {errors.New("unexpected"), codes.Internal}} {
		l.mu.Lock()
		l.err = tt.err
		l.mu.Unlock()
		_, err := client.AddAlert(context.Background(), &screenv1.Alert{Color: "red", Message: "x"})
		if got := status.Code(err); got != tt.code {
			t.Fatalf("error %v mapped to %s", tt.err, got)
		}
	}
}
