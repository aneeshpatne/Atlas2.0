package newsworker

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type Refresher interface{ Refresh(context.Context) error }
type Worker interface {
	Run(context.Context, <-chan struct{}) error
}

type Scheduled struct {
	refresher Refresher
	interval  time.Duration
	logger    *slog.Logger
}

func New(refresher Refresher, interval time.Duration, logger *slog.Logger) *Scheduled {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduled{refresher: refresher, interval: interval, logger: logger}
}

func (w *Scheduled) Run(ctx context.Context, refreshNow <-chan struct{}) error {
	w.refresh(ctx)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.refresh(ctx)
		case <-refreshNow:
			w.refresh(ctx)
		}
	}
}

func (w *Scheduled) refresh(ctx context.Context) {
	if err := w.refresher.Refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
		w.logger.Error("news refresh failed", "error", err)
	}
}
