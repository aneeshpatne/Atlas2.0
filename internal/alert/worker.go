package alert

import (
	"context"
	"time"
)

func Run(ctx context.Context, presenter Presenter, item Alert, duration, clearTimeout time.Duration) error {
	if err := presenter.Show(ctx, item); err != nil {
		return err
	}
	t := time.NewTimer(duration)
	defer t.Stop()
	select {
	case <-ctx.Done():
		clearCtx, cancel := context.WithTimeout(context.Background(), clearTimeout)
		defer cancel()
		_ = presenter.Clear(clearCtx)
		return ctx.Err()
	case <-t.C:
		return presenter.Clear(ctx)
	}
}
