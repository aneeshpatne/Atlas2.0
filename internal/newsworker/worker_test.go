package newsworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeRefresh struct {
	mu     sync.Mutex
	n      int
	notify chan struct{}
	err    error
}

func (f *fakeRefresh) Refresh(context.Context) error {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return f.err
}
func TestWorkerImmediateRequestedPeriodicAndCancel(t *testing.T) {
	f := &fakeRefresh{notify: make(chan struct{}, 10)}
	w := New(f, 20*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	refresh := make(chan struct{}, 1)
	go func() { done <- w.Run(ctx, refresh) }()
	mustSignal(t, f.notify)
	refresh <- struct{}{}
	mustSignal(t, f.notify)
	mustSignal(t, f.notify)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("worker did not stop promptly")
	}
}
func TestWorkerSurvivesRefreshError(t *testing.T) {
	f := &fakeRefresh{notify: make(chan struct{}, 2), err: errors.New("boom")}
	w := New(f, time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, make(chan struct{})) }()
	mustSignal(t, f.notify)
	cancel()
	<-done
}
func mustSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for refresh")
	}
}
