package newsworkflow

import (
	"context"
	"errors"
	"time"

	"github.com/aneeshpatne/atlas/internal/dashboard"
)

// Clock is the default display between news passes.
// ShowClock paints one frame (clear + clock). RunClock keeps it updating
// until the context is cancelled.
type Clock interface {
	ShowClock(context.Context) error
	RunClock(context.Context) error
}

// Options controls how each genre-wise news pass is painted.
// Pass cadence is driven externally (cron wall-clock ticks and NotifyNewsChanged)
// via the refreshNow channel passed to Run. Between passes the clock is shown.
type Options struct {
	GenreHold          time.Duration
	StoryHold          time.Duration
	MaxStoriesPerGenre int
	Genre              string
	FontPath           string
	AssetsDir          string
	AllowPrivateImages bool
}

type Worker struct {
	dashboard *dashboard.Dashboard
	clock     Clock
	options   Options
}

func New(d *dashboard.Dashboard, clock Clock, options Options) *Worker {
	return &Worker{dashboard: d, clock: clock, options: options}
}

// Run keeps the clock as the default screen. On each refreshNow signal (cron
// quarter-hour or news-changed), it runs one news pass, then clears and
// restores the clock so time is visible again as soon as news ends.
func (w *Worker) Run(ctx context.Context, refreshNow <-chan struct{}) error {
	if w.dashboard == nil {
		return errors.New("news workflow: dashboard is required")
	}

	dashboardOptions := dashboard.Options{
		Genre:              w.options.Genre,
		FontPath:           w.options.FontPath,
		AssetsDir:          w.options.AssetsDir,
		AllowPrivateImages: w.options.AllowPrivateImages,
		GenreHold:          w.options.GenreHold,
		StoryHold:          w.options.StoryHold,
		MaxStoriesPerGenre: w.options.MaxStoriesPerGenre,
	}

	// pending means a pass was requested while news (or a brief clock flash)
	// was already in progress — run another pass after restoring the clock.
	pending := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if pending {
			// Always put the clock back first so the last news frame does not linger.
			if err := w.paintClockOnce(ctx); err != nil {
				return err
			}
			pending = false
		} else {
			// Default state: live clock until the next scheduled (or forced) pass.
			if err := w.runClockUntil(ctx, refreshNow); err != nil {
				return err
			}
		}

		cycleCtx, cancel := context.WithCancel(ctx)
		cycleDone := make(chan error, 1)
		go func() { cycleDone <- w.dashboard.ShowNewsCycle(cycleCtx, dashboardOptions) }()

	cycleWait:
		for {
			select {
			case <-ctx.Done():
				cancel()
				<-cycleDone
				return ctx.Err()
			case <-refreshNow:
				// Finish the current pass; run another after clock is restored.
				pending = true
			case err := <-cycleDone:
				cancel()
				if err != nil && !errors.Is(err, context.Canceled) {
					return err
				}
				break cycleWait
			}
		}
		// Next iteration restores the clock ASAP: either a one-shot paint
		// before a coalesced follow-up pass, or live clock until the next tick.
	}
}

func (w *Worker) paintClockOnce(ctx context.Context) error {
	if w.clock == nil {
		return nil
	}
	if err := w.clock.ShowClock(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return ctx.Err()
}

// runClockUntil shows a live updating clock until refreshNow fires or ctx ends.
func (w *Worker) runClockUntil(ctx context.Context, refreshNow <-chan struct{}) error {
	if w.clock == nil {
		return waitForRefresh(ctx, refreshNow)
	}

	clockCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.clock.RunClock(clockCtx) }()

	select {
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	case <-refreshNow:
		cancel()
		<-done
		return nil
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		// Clock exited without a pass request (e.g. cancelled child); wait for signal.
		return waitForRefresh(ctx, refreshNow)
	}
}

func waitForRefresh(ctx context.Context, refreshNow <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-refreshNow:
		return nil
	}
}
