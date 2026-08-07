package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aneeshpatne/atlas/internal/alert"
	"github.com/aneeshpatne/atlas/internal/config"
)

type fakeDisplay struct {
	mu         sync.Mutex
	running    bool
	events     []string
	startBlock <-chan struct{}
	startErr   error
	stopErr    error
}

func (f *fakeDisplay) add(v string) { f.mu.Lock(); defer f.mu.Unlock(); f.events = append(f.events, v) }
func (f *fakeDisplay) Start(context.Context) error {
	if f.startBlock != nil {
		<-f.startBlock
	}
	if f.startErr != nil {
		return f.startErr
	}
	f.mu.Lock()
	f.running = true
	f.events = append(f.events, "display start")
	f.mu.Unlock()
	return nil
}

func TestExpiredQueuedAlertIsNotAccepted(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	block := make(chan struct{})
	d := &fakeDisplay{startBlock: block}
	n := &fakeNews{display: d, starts: make(chan struct{}, 1)}
	p := &fakePresenter{display: d, shown: make(chan string, 1), cleared: make(chan struct{}, 1)}
	s := New(root, testConfig(), d, n, p, nil)
	s.Start()
	reconciled := make(chan error, 1)
	go func() { reconciled <- s.Reconcile(context.Background(), true) }()
	time.Sleep(10 * time.Millisecond)
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := s.AddAlert(ctx, alert.Alert{OperationID: "late", Message: "late"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AddAlert error = %v, want deadline", err)
	}
	close(block)
	mustNoErr(t, <-reconciled)
	time.Sleep(20 * time.Millisecond)
	select {
	case shown := <-p.shown:
		t.Fatalf("expired alert was shown: %s", shown)
	default:
	}
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	mustNoErr(t, s.Stop(shutdown))
}

func TestStartupFailureIsReturned(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig()
	cfg.StartupRetryCount = 1
	d := &fakeDisplay{startErr: errors.New("offline")}
	s := New(root, cfg, d, &fakeNews{display: d, starts: make(chan struct{}, 1)}, &fakePresenter{display: d, shown: make(chan string, 1), cleared: make(chan struct{}, 1)}, nil)
	s.Start()
	if err := s.Reconcile(context.Background(), true); err == nil {
		t.Fatal("expected startup reconciliation failure")
	}
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	mustNoErr(t, s.Stop(shutdown))
}

func TestNewsTickDoesNotRepeatedlyStopOffDisplay(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &fakeDisplay{}
	s := New(root, testConfig(), d, &fakeNews{display: d, starts: make(chan struct{}, 1)}, &fakePresenter{display: d, shown: make(chan string, 1), cleared: make(chan struct{}, 1)}, nil)
	s.Start()
	mustNoErr(t, s.Reconcile(context.Background(), false))
	s.RequestNewsPass()
	_, err := s.Snapshot(context.Background())
	mustNoErr(t, err)
	d.mu.Lock()
	stops := 0
	for _, event := range d.events {
		if event == "display stop" {
			stops++
		}
	}
	d.mu.Unlock()
	if stops != 1 {
		t.Fatalf("display stopped %d times, want once", stops)
	}
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	mustNoErr(t, s.Stop(shutdown))
}

func TestFailedWorkerUsesBackoff(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &fakeDisplay{}
	n := &failingNews{starts: make(chan struct{}, 3)}
	s := New(root, testConfig(), d, n, &fakePresenter{display: d, shown: make(chan string, 1), cleared: make(chan struct{}, 1)}, nil)
	s.Start()
	mustNoErr(t, s.Reconcile(context.Background(), true))
	mustRecv(t, n.starts)
	time.Sleep(100 * time.Millisecond)
	select {
	case <-n.starts:
		t.Fatal("worker restarted without backoff")
	default:
	}
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	mustNoErr(t, s.Stop(shutdown))
}

func (f *fakeDisplay) Stop(context.Context) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	f.mu.Lock()
	f.running = false
	f.events = append(f.events, "display stop")
	f.mu.Unlock()
	return nil
}

func TestStopReturnsDisplayFailure(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &fakeDisplay{}
	n := &fakeNews{display: d, starts: make(chan struct{}, 1)}
	s := New(root, testConfig(), d, n, &fakePresenter{display: d, shown: make(chan string, 1), cleared: make(chan struct{}, 1)}, nil)
	s.Start()
	mustNoErr(t, s.Reconcile(context.Background(), true))
	mustRecv(t, n.starts)
	d.stopErr = errors.New("clear failed")
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := s.Stop(shutdown); err == nil || !strings.Contains(err.Error(), "clear failed") {
		t.Fatalf("Stop error = %v, want display failure", err)
	}
}
func (f *fakeDisplay) IsRunning(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, nil
}

type fakeNews struct {
	display *fakeDisplay
	starts  chan struct{}
}

type failingNews struct{ starts chan struct{} }

func (f *failingNews) Run(context.Context, <-chan struct{}) error {
	f.starts <- struct{}{}
	return errors.New("redis unavailable")
}

func (f *fakeNews) Run(ctx context.Context, _ <-chan struct{}) error {
	f.display.add("news start")
	select {
	case f.starts <- struct{}{}:
	default:
	}
	<-ctx.Done()
	f.display.add("news stop")
	return ctx.Err()
}

type fakePresenter struct {
	display *fakeDisplay
	shown   chan string
	cleared chan struct{}
}

func (f *fakePresenter) Show(_ context.Context, a alert.Alert) error {
	f.display.add("alert " + a.Message)
	f.shown <- a.Message
	return nil
}
func (f *fakePresenter) Clear(context.Context) error {
	f.display.add("alert clear")
	select {
	case f.cleared <- struct{}{}:
	default:
	}
	return nil
}
func testConfig() config.Config {
	c := config.Default()
	c.AlertDisplayDuration = 15 * time.Millisecond
	c.WorkerStopTimeout = 100 * time.Millisecond
	c.OperationTimeout = 100 * time.Millisecond
	c.StartupRetryDelay = time.Millisecond
	return c
}
func TestAlertsInterruptQueueFIFOAndResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &fakeDisplay{}
	n := &fakeNews{display: d, starts: make(chan struct{}, 5)}
	p := &fakePresenter{display: d, shown: make(chan string, 5), cleared: make(chan struct{}, 5)}
	s := New(ctx, testConfig(), d, n, p, nil)
	s.Start()
	mustNoErr(t, s.Reconcile(context.Background(), true))
	mustRecv(t, n.starts)
	for i, msg := range []string{"A", "B", "C"} {
		r, err := s.AddAlert(context.Background(), alert.Alert{OperationID: msg, Message: msg})
		mustNoErr(t, err)
		if i == 0 && r.State != "displaying" {
			t.Fatalf("first state %s", r.State)
		}
		if i > 0 && r.State != "queued" {
			t.Fatalf("queued state %s", r.State)
		}
	}
	for _, want := range []string{"A", "B", "C"} {
		select {
		case got := <-p.shown:
			if got != want {
				t.Fatalf("got %s want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("alert timeout")
		}
	}
	mustRecv(t, n.starts)
	shutdown, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	mustNoErr(t, s.Stop(shutdown))
	d.mu.Lock()
	events := append([]string(nil), d.events...)
	d.mu.Unlock()
	newsStop := -1
	alertA := -1
	for i, e := range events {
		if e == "news stop" && newsStop < 0 {
			newsStop = i
		}
		if e == "alert A" {
			alertA = i
		}
	}
	if newsStop < 0 || alertA < 0 || newsStop > alertA {
		t.Fatalf("invalid event order: %v", events)
	}
}
func TestScheduledShutdownCancelsAlertAndDoesNotResumeNews(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := testConfig()
	c.AlertDisplayDuration = time.Hour
	d := &fakeDisplay{}
	n := &fakeNews{display: d, starts: make(chan struct{}, 5)}
	p := &fakePresenter{display: d, shown: make(chan string, 5), cleared: make(chan struct{}, 5)}
	s := New(ctx, c, d, n, p, nil)
	s.Start()
	mustNoErr(t, s.Reconcile(context.Background(), true))
	mustRecv(t, n.starts)
	_, err := s.AddAlert(context.Background(), alert.Alert{OperationID: "A", Message: "A"})
	mustNoErr(t, err)
	select {
	case <-p.shown:
	case <-time.After(time.Second):
		t.Fatal("alert timeout")
	}
	s.RequestScheduledShutdown()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snap, _ := s.Snapshot(context.Background())
		if snap.Lifecycle == StateOff && !snap.DisplayRunning {
			if snap.NewsRunning || snap.AlertRunning || snap.QueuedAlerts != 0 {
				t.Fatalf("bad state %+v", snap)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("did not shut down")
}
func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func mustRecv(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}
