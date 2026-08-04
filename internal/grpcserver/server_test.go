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
	ack, err := client.AddNews(ctx, &screenv1.NewsItem{Title: "Title", Genre: "india"})
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.State != "stored" || ack.OperationId == "" {
		t.Fatalf("bad ack %+v", ack)
	}
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
func TestGRPCValidationAndErrorMapping(t *testing.T) {
	l := &testLifecycle{}
	client, closeFn := newClient(t, l, &testStore{})
	defer closeFn()
	tests := []struct {
		name string
		call func() error
		code codes.Code
	}{{"nil news", func() error { _, e := client.AddNews(context.Background(), nil); return e }, codes.InvalidArgument}, {"empty title", func() error { _, e := client.AddNews(context.Background(), &screenv1.NewsItem{Genre: "x"}); return e }, codes.InvalidArgument}, {"empty color", func() error { _, e := client.AddAlert(context.Background(), &screenv1.Alert{Message: "x"}); return e }, codes.InvalidArgument}, {"empty message", func() error { _, e := client.AddAlert(context.Background(), &screenv1.Alert{Color: "red"}); return e }, codes.InvalidArgument}}
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
