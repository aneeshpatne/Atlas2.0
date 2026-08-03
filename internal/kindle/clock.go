package kindle

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultClockFont = "/usr/java/lib/fonts/Helvetica_LT_65_Medium.ttf"

// ClockOptions controls the clock's appearance. Zero values select defaults
// sized relative to the display, so the mode works across Kindle resolutions.
type ClockOptions struct {
	FontPath string
	FontSize int
	Now      func() time.Time
}

// RunClock switches the Kindle to landscape, clears the display, sets the
// front light to 20, then updates a centered HH:MM clock once per minute.
// Each update touches only the clock rectangle and performs a single
// non-flashing GC16 regional refresh. GC16 is slower than DU, but avoids
// the cumulative ghosting that a clock running for hours would otherwise show.
func (d *Device) RunClock(ctx context.Context, options ClockOptions) error {
	if options.FontPath == "" {
		options.FontPath = defaultClockFont
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	if err := d.SetRotation("horizontal"); err != nil {
		return err
	}
	if err := d.ClearScreen(); err != nil {
		return err
	}
	if err := d.SetBacklight(20); err != nil {
		return err
	}
	width, height, err := d.displaySize()
	if err != nil {
		return err
	}
	if options.FontSize == 0 {
		options.FontSize = height * 42 / 100
	}
	if options.FontSize < 1 {
		return fmt.Errorf("clock: font size must be positive")
	}

	// A stable, generously padded box means glyphs from the previous minute are
	// always erased, including when proportional digits have different widths.
	boxHeight := height * 58 / 100
	boxTop := (height - boxHeight) / 2
	box := displayRect{top: boxTop, left: 0, width: width, height: boxHeight}

	for {
		now := options.Now()
		if err := d.drawClock(now.Format("15:04"), options, box, height); err != nil {
			return err
		}

		nextMinute := now.Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(nextMinute.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

type displayRect struct{ top, left, width, height int }

func (d *Device) displaySize() (int, int, error) {
	output, err := d.client.Run("/mnt/us/usbnet/bin/fbink -e")
	if err != nil {
		return 0, 0, fmt.Errorf("clock: get display size: %w", err)
	}
	matches := fbinkScreenSize.FindStringSubmatch(output)
	if len(matches) != 3 {
		return 0, 0, fmt.Errorf("clock: unexpected display size %q", strings.TrimSpace(output))
	}
	width, widthErr := strconv.Atoi(matches[1])
	height, heightErr := strconv.Atoi(matches[2])
	if widthErr != nil || heightErr != nil || width < 1 || height < 1 {
		return 0, 0, fmt.Errorf("clock: invalid display size %q", strings.TrimSpace(output))
	}
	return width, height, nil
}

var fbinkScreenSize = regexp.MustCompile(`screenWidth=([0-9]+);screenHeight=([0-9]+)`)

func (d *Device) drawClock(value string, options ClockOptions, box displayRect, screenHeight int) error {
	bottom := screenHeight - box.top - box.height
	region := fmt.Sprintf("top=%d,left=%d,width=%d,height=%d", box.top, box.left, box.width, box.height)
	typeSpec := fmt.Sprintf("regular=%s,px=%d,top=%d,bottom=%d,left=%d,right=%d,padding=BOTH",
		options.FontPath, options.FontSize, box.top, bottom, box.left, box.left)

	// Both writes are framebuffer-only. The final command refreshes exactly the
	// clock rectangle, preventing intermediate white flashes and screen-wide wear.
	command := fmt.Sprintf(
		`/mnt/us/usbnet/bin/fbink -q -b -B WHITE -k %s && /mnt/us/usbnet/bin/fbink -q -b -B WHITE -C BLACK -m -M -t %s %s && /mnt/us/usbnet/bin/fbink -q -W GC16 -s %s`,
		region, shellQuote(typeSpec), shellQuote(value), region,
	)
	if _, err := d.client.Run(command); err != nil {
		return fmt.Errorf("clock: draw %q: %w", value, err)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
