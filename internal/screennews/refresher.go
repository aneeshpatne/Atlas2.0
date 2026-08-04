package screennews

import (
	"context"
	"sync"

	"github.com/aneeshpatne/atlas/internal/display"
)

type Refresher struct {
	store   Repository
	display display.Controller
	mu      sync.Mutex
	next    int
}

func NewRefresher(store Repository, d display.Controller) *Refresher {
	return &Refresher{store: store, display: d}
}
func (r *Refresher) Refresh(ctx context.Context) error {
	items, err := r.store.List(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return r.display.RenderNews(ctx, nil)
	}
	r.mu.Lock()
	i := r.next % len(items)
	r.next = (i + 1) % len(items)
	r.mu.Unlock()
	return r.display.RenderNews(ctx, items[i:i+1])
}
