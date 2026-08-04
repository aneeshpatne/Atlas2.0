package kindledisplay

import (
	"context"
	"strings"
	"sync"
	"time"

	screenv1 "github.com/aneeshpatne/atlas/gen/screen/v1"
	"github.com/aneeshpatne/atlas/internal/alert"
	"github.com/aneeshpatne/atlas/internal/display"
	"github.com/aneeshpatne/atlas/internal/kindle"
)

type Controller struct {
	device   *kindle.Device
	location *time.Location
	mu       sync.Mutex
	running  bool
}

func New(device *kindle.Device) *Controller { return NewWithLocation(device, time.Local) }

func NewWithLocation(device *kindle.Device, location *time.Location) *Controller {
	if location == nil {
		location = time.Local
	}
	return &Controller{device: device, location: location}
}
func (c *Controller) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	brightness, err := c.device.GetBrightness()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if brightness > 0 {
		c.running = true
		return nil
	}
	if err := c.device.SetRotation("horizontal"); err != nil {
		return err
	}
	if err := c.device.ClearScreen(); err != nil {
		return err
	}
	if err := c.device.SetBacklight(20); err != nil {
		return err
	}
	c.running = true
	return nil
}
// Stop clears the e-ink panel and sets brightness to 0. Used on scheduled
// window end and process exit (SIGTERM/SIGINT).
func (c *Controller) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Always attempt clear + zero backlight so SIGTERM / scheduled stop leave
	// the panel blank and dark even if our running flag is stale.
	if err := c.device.ClearScreen(); err != nil {
		return err
	}
	if err := c.device.SetBacklight(0); err != nil {
		return err
	}
	c.running = false
	return nil
}
func (c *Controller) IsRunning(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	brightness, err := c.device.GetBrightness()
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	c.running = brightness > 0
	c.mu.Unlock()
	return brightness > 0, nil
}
func (c *Controller) RenderNews(ctx context.Context, items []*screenv1.NewsItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if !running {
		return display.ErrNotRunning
	}
	if err := c.device.ClearScreen(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	item := items[0]
	if err := c.device.ShowTitle(item.Title); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.Description != "" {
		if err := c.device.ShowDescription(item.Description); err != nil {
			return err
		}
	}
	domains := make([]string, 0, len(item.Sources))
	for _, s := range item.Sources {
		if s != nil && s.Domain != "" {
			domains = append(domains, s.Domain)
		}
	}
	if len(domains) > 0 {
		return c.device.ShowDomains(strings.Join(domains, " · "))
	}
	return nil
}

func (c *Controller) clockOptions() kindle.ClockOptions {
	return kindle.ClockOptions{Now: func() time.Time {
		return time.Now().In(c.location)
	}}
}

// ShowClock clears the screen and paints one clock frame (default idle restore).
func (c *Controller) ShowClock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if !running {
		return display.ErrNotRunning
	}
	return c.device.ShowClockOnce(ctx, c.clockOptions())
}

// RunClock keeps the clock updating until ctx is cancelled. This is the default
// screen between scheduled news passes.
func (c *Controller) RunClock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if !running {
		return display.ErrNotRunning
	}
	return c.device.RunClock(ctx, c.clockOptions())
}

func (c *Controller) Show(ctx context.Context, a alert.Alert) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if !running {
		return display.ErrNotRunning
	}
	if err := c.device.ClearScreen(); err != nil {
		return err
	}
	if err := c.device.ShowTitle(a.Message); err != nil {
		return err
	}
	return ctx.Err()
}
func (c *Controller) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.device.ClearScreen()
}
