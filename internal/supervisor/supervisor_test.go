package supervisor

import (
	"context"
	"sync"
	"testing"
	"time"

	screenv1 "github.com/aneeshpatne/atlas/gen/screen/v1"
	"github.com/aneeshpatne/atlas/internal/alert"
	"github.com/aneeshpatne/atlas/internal/config"
)

type fakeDisplay struct {
	mu      sync.Mutex
	running bool
	events  []string
}

func (f *fakeDisplay) add(v string) { f.mu.Lock(); defer f.mu.Unlock(); f.events = append(f.events, v) }
func (f *fakeDisplay) Start(context.Context) error {
	f.mu.Lock()
	f.running = true
	f.events = append(f.events, "display start")
	f.mu.Unlock()
	return nil
}
func (f *fakeDisplay) Stop(context.Context) error {
	f.mu.Lock()
	f.running = false
	f.events = append(f.events, "display stop")
	f.mu.Unlock()
	return nil
}
func (f *fakeDisplay) IsRunning(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, nil
}
func (f *fakeDisplay) RenderNews(context.Context, []*screenv1.NewsItem) error { return nil }

type fakeNews struct {
	display *fakeDisplay
	starts  chan struct{}
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
