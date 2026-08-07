package newsworkflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	showN atomic.Int32
	runN  atomic.Int32
	// block RunClock until released or ctx done
	started chan struct{}
}

func (f *fakeClock) ShowClock(ctx context.Context) error {
	f.showN.Add(1)
	return ctx.Err()
}

func (f *fakeClock) RunClock(ctx context.Context) error {
	f.runN.Add(1)
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestWaitForRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- waitForRefresh(ctx, ch) }()
	select {
	case <-done:
		t.Fatal("returned before signal")
	case <-time.After(20 * time.Millisecond):
	}
	ch <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForRefreshCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- waitForRefresh(ctx, make(chan struct{})) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancel error")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestRunRequiresDashboard(t *testing.T) {
	w := New(nil, nil, Options{})
	err := w.Run(context.Background(), make(chan struct{}))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunClockUntilCancelsOnRefresh(t *testing.T) {
	clock := &fakeClock{started: make(chan struct{}, 1)}
	w := New(nil, clock, Options{}) // dashboard nil only used by Run, not runClockUntil
	// can't call Run with nil dashboard; test runClockUntil via package function pattern
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refresh := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- w.runClockUntil(ctx, refresh) }()
	select {
	case <-clock.started:
	case <-time.After(time.Second):
		t.Fatal("clock did not start")
	}
	refresh <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	if clock.runN.Load() != 1 {
		t.Fatalf("runN=%d", clock.runN.Load())
	}
}

func TestPaintClockOnce(t *testing.T) {
	clock := &fakeClock{}
	w := New(nil, clock, Options{})
	if err := w.paintClockOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if clock.showN.Load() != 1 {
		t.Fatalf("showN=%d", clock.showN.Load())
	}
}

func TestPaintClockOnceNilClock(t *testing.T) {
	w := New(nil, nil, Options{})
	if err := w.paintClockOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunClockUntilNilClockWaitsForRefresh(t *testing.T) {
	w := New(nil, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refresh := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- w.runClockUntil(ctx, refresh) }()
	time.Sleep(20 * time.Millisecond)
	refresh <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}
