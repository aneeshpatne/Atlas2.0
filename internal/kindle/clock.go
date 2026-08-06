package kindle

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultClockFont = "/usr/java/lib/fonts/Helvetica_LT_65_Medium.ttf"
const clockTimeFont = "/mnt/us/fonts/InstrumentSerif-Regular.ttf"

// ClockOptions controls the clock's appearance. Zero values select defaults
// sized relative to the display, so the mode works across Kindle resolutions.
type ClockOptions struct {
	FontPath string
	FontSize int
	Now      func() time.Time
	Metrics  MetricsProvider
}

type MetricsSnapshot struct {
	Climate, Temperature, Pressure, Humidity, PM25 string
}

type MetricsProvider interface {
	ReadMetrics(context.Context) (MetricsSnapshot, error)
}

// RunClock switches the Kindle to landscape, clears the display, sets the
// front light to 20, then updates a centered HH:MM clock once per minute.
// Each update touches only the clock rectangle and performs a single
// non-flashing GC16 regional refresh. GC16 is slower than DU, but avoids
// the cumulative ghosting that a clock running for hours would otherwise show.
func (d *Device) RunClock(ctx context.Context, options ClockOptions) error {
	options, layout, width, height, err := d.initializeClock(ctx, options)
	if err != nil {
		return err
	}
	lastDate := ""
	lastClock := ""

	for {
		now := options.Now()
		date := now.Format("Monday, January 2")
		if date != lastDate {
			if err := d.drawDateContext(ctx, date, options, layout, width, height); err != nil {
				return err
			}
			lastDate = date
		}
		clock := now.Format("3:04 PM")
		if clock != lastClock {
			if err := d.drawClockContext(ctx, now, options, layout, width, height); err != nil {
				return err
			}
			lastClock = clock
		}
		nextTick := now.Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(nextTick.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (d *Device) initializeClock(ctx context.Context, options ClockOptions) (ClockOptions, dashboardLayout, int, int, error) {
	if options.FontPath == "" {
		options.FontPath = defaultClockFont
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	if err := d.SetRotationContext(ctx, "horizontal"); err != nil {
		return options, dashboardLayout{}, 0, 0, err
	}
	if err := d.ClearScreenContext(ctx); err != nil {
		return options, dashboardLayout{}, 0, 0, err
	}
	if err := d.SetBacklightContext(ctx, 20); err != nil {
		return options, dashboardLayout{}, 0, 0, err
	}
	width, height, err := d.displaySizeContext(ctx)
	if err != nil {
		return options, dashboardLayout{}, 0, 0, err
	}

	layout := newDashboardLayout(width, height)
	// Prefer the largest face that still fits the clock band. drawClock may
	// shrink slightly for long times ("12:59") so AM/PM stays visible.
	if options.FontSize == 0 {
		options.FontSize = layout.clock.height * 98 / 100
	}
	if options.FontSize < 1 {
		return options, dashboardLayout{}, 0, 0, fmt.Errorf("clock: font size must be positive")
	}
	maxFont := layout.clock.height * 98 / 100
	if options.FontSize > maxFont {
		options.FontSize = maxFont
	}

	if err := d.drawStaticChromeContext(ctx, options, layout, width, height); err != nil {
		return options, dashboardLayout{}, 0, 0, err
	}
	return options, layout, width, height, nil
}

// ShowClockOnce paints the clock dashboard once and returns. It reuses the
// same initialization and frame-rendering path as RunClock.
func (d *Device) ShowClockOnce(ctx context.Context, options ClockOptions) error {
	options, layout, width, height, err := d.initializeClock(ctx, options)
	if err != nil {
		return err
	}
	now := options.Now()
	if err := d.drawDateContext(ctx, now.Format("Monday, January 2"), options, layout, width, height); err != nil {
		return err
	}
	return d.drawClockContext(ctx, now, options, layout, width, height)
}

type displayRect struct{ top, left, width, height int }

// dashboardLayout keeps the display deliberately editorial:
//
//	[ large centered time + PM ]
//	        ——— solid rule ———
//	      Monday, August 3
//	····························
//	          Climate: Warm
//	TEMP | PRESS | HUMID | PM2.5
type dashboardLayout struct {
	clock, clockRule, date, divider, climate displayRect
	metrics                                  [4]displayRect
	metricRules                              [4]displayRect
	metricDividers                           [3]displayRect
}

func newDashboardLayout(width, height int) dashboardLayout {
	// FBInk rejects 1px refresh regions as "bogus empty" (softlock guard).
	line := max(2, height*2/1000)
	marginX := width * 5 / 100
	contentW := width - 2*marginX

	// Four equal metric columns with quiet gutters and a short divider. The
	// extra whitespace makes the lower strip feel lighter than the hero clock.
	divW := max(2, width*2/1000)
	gutter := max(divW+4, width*6/1000)
	colW := (contentW - 3*gutter) / 4

	// Metrics strip stays tall enough for large reading digits; date and
	// climate (remark) claim a bit more vertical room so their type can grow.
	metricsTop := height * 76 / 100
	metricsH := height * 23 / 100
	// Label band is intentionally short; the rule is a typographic underline.
	ruleTop := metricsTop + metricsH*19/100
	var metrics [4]displayRect
	var metricRules [4]displayRect
	var metricDividers [3]displayRect
	for i := 0; i < 4; i++ {
		left := marginX + i*(colW+gutter)
		metrics[i] = displayRect{top: metricsTop, left: left, width: colW, height: metricsH}
		ruleW := max(2, colW*48/100)
		metricRules[i] = displayRect{
			top: ruleTop, left: left + (colW-ruleW)/2,
			width: ruleW, height: line,
		}
		if i < 3 {
			metricDividers[i] = displayRect{
				top:    metricsTop + metricsH*16/100,
				left:   left + colW + (gutter-divW)/2,
				width:  divW,
				height: max(2, metricsH*68/100),
			}
		}
	}

	ruleW := width * 38 / 100
	// Keep the hero clock broad so wide faces ("12:59") stay generous, but
	// leave a visible edge margin so the composition does not touch the bezel.
	clockMargin := width * 1 / 100
	// Date + climate get taller bands so font sizes are not capped tiny by
	// drawTextRegion's box-height limit (box.h * 96%).
	dateTop := height * 56 / 100
	dateH := height * 9 / 100
	dividerTop := height * 66 / 100
	climateTop := height * 67 / 100
	climateH := height * 8 / 100
	return dashboardLayout{
		// Slightly shorter hero clock to fund larger date / climate type.
		clock: displayRect{
			top: height * 0 / 100, left: clockMargin,
			width: width - 2*clockMargin, height: height * 55 / 100,
		},
		clockRule: displayRect{
			top: height * 55 / 100, left: (width - ruleW) / 2,
			width: ruleW, height: line,
		},
		date: displayRect{
			top: dateTop, left: marginX,
			width: contentW, height: dateH,
		},
		divider: displayRect{
			top: dividerTop, left: marginX,
			width: contentW, height: line,
		},
		climate: displayRect{
			top: climateTop, left: marginX,
			width: contentW, height: climateH,
		},
		metrics:        metrics,
		metricRules:    metricRules,
		metricDividers: metricDividers,
	}
}

func (d *Device) displaySize() (int, int, error) {
	return d.displaySizeContext(context.Background())
}

func (d *Device) displaySizeContext(ctx context.Context) (int, int, error) {
	d.mu.Lock()
	if d.displayWidth > 0 && d.displayHeight > 0 {
		width, height := d.displayWidth, d.displayHeight
		d.mu.Unlock()
		return width, height, nil
	}
	d.mu.Unlock()
	output, err := d.run(ctx, "/mnt/us/usbnet/bin/fbink -e")
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
	d.mu.Lock()
	d.displayWidth, d.displayHeight = width, height
	d.mu.Unlock()
	return width, height, nil
}

var fbinkScreenSize = regexp.MustCompile(`screenWidth=([0-9]+);screenHeight=([0-9]+)`)

const fbinkPath = "/mnt/us/usbnet/bin/fbink"

// drawStaticChrome paints the non-clock chrome once at startup: rules,
// climate row, metric labels/placeholders, and column dividers.
func (d *Device) drawStaticChrome(options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
	return d.drawStaticChromeContext(context.Background(), options, layout, screenWidth, screenHeight)
}

func (d *Device) drawStaticChromeContext(ctx context.Context, options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
	readings := MetricsSnapshot{Climate: "--", Temperature: "--", Pressure: "--", Humidity: "--", PM25: "--"}
	if options.Metrics != nil {
		if provided, err := options.Metrics.ReadMetrics(ctx); err != nil {
			slog.Warn("clock metrics unavailable", "error", err)
		} else {
			readings = fillMissingMetrics(provided)
		}
	}
	if err := d.fillRectContext(ctx, layout.clockRule, true); err != nil {
		return fmt.Errorf("clock: clock rule: %w", err)
	}
	if err := d.drawDottedLineContext(ctx, layout.divider); err != nil {
		return fmt.Errorf("clock: divider: %w", err)
	}
	// Keep label and value together so the row reads as one centered status.
	// Target ~9% of screen height (capped to the climate band).
	if err := d.drawTextRegionContext(ctx, "climate", "Climate: "+readings.Climate, options.FontPath, screenHeight*9/100, layout.climate, screenWidth, screenHeight, true); err != nil {
		return err
	}

	// Dummy readings until live sensors are wired in. Number and unit are
	// drawn separately so the digits can fill most of the column height.
	metrics := []struct {
		name   string
		label  string
		number string
		unit   string
	}{
		{"temperature", "TEMP", readings.Temperature, "°C"},
		{"pressure", "PRESS", readings.Pressure, "hPa"},
		{"humidity", "HUMID", readings.Humidity, "%"},
		{"pm25", "PM2.5", readings.PM25, "µg/m³"},
	}
	labelSize := screenHeight * 25 / 1000
	unitSize := screenHeight * 24 / 1000
	for i, metric := range metrics {
		box := layout.metrics[i]
		// Compact label + rule; most of the column is the reading.
		labelBox := displayRect{
			top: box.top, left: box.left,
			width: box.width, height: box.height * 16 / 100,
		}
		if err := d.drawTextRegionContext(ctx, metric.name+" label", metric.label, options.FontPath, labelSize, labelBox, screenWidth, screenHeight, true); err != nil {
			return err
		}
		if err := d.fillRectContext(ctx, layout.metricRules[i], true); err != nil {
			return fmt.Errorf("clock: %s rule: %w", metric.name, err)
		}
		// Huge digits — size from the number band, then fit column width.
		numberBox := displayRect{
			top: box.top + box.height*25/100, left: box.left,
			width: box.width, height: box.height * 46 / 100,
		}
		numberSize := numberBox.height * 94 / 100
		// ~0.58em per digit; shrink if the reading is wider than the column.
		if glyphs := len([]rune(metric.number)); glyphs > 0 {
			if maxByWidth := numberBox.width * 100 / (58 * glyphs); numberSize > maxByWidth {
				numberSize = maxByWidth
			}
		}
		if err := d.drawTextRegionContext(ctx, metric.name+" number", metric.number, options.FontPath, numberSize, numberBox, screenWidth, screenHeight, true); err != nil {
			return err
		}
		unitBox := displayRect{
			top: box.top + box.height*76/100, left: box.left,
			width: box.width, height: box.height * 18 / 100,
		}
		if err := d.drawTextRegionContext(ctx, metric.name+" unit", metric.unit, options.FontPath, unitSize, unitBox, screenWidth, screenHeight, true); err != nil {
			return err
		}
	}
	for i, div := range layout.metricDividers {
		if err := d.fillRectContext(ctx, div, true); err != nil {
			return fmt.Errorf("clock: metric divider %d: %w", i, err)
		}
	}
	return nil
}

func fillMissingMetrics(value MetricsSnapshot) MetricsSnapshot {
	for field, current := range map[*string]string{
		&value.Climate: value.Climate, &value.Temperature: value.Temperature,
		&value.Pressure: value.Pressure, &value.Humidity: value.Humidity, &value.PM25: value.PM25,
	} {
		if strings.TrimSpace(current) == "" {
			*field = "--"
		}
	}
	return value
}

func (d *Device) drawClock(now time.Time, options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
	return d.drawClockContext(context.Background(), now, options, layout, screenWidth, screenHeight)
}

func (d *Device) drawClockContext(ctx context.Context, now time.Time, options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
	box := layout.clock
	region := regionSpec(box)

	timeText := now.Format("3:04")
	periodText := now.Format("PM")
	glyphs := len([]rune(timeText))

	// Keep the period close and quiet: the main time should own the hierarchy.
	periodSize := max(1, box.height*10/100)
	periodWidth := periodSize * 20 / 10
	gap := max(box.width*6/1000, 6)
	timeAvailW := box.width - gap - periodWidth
	if timeAvailW < box.width/2 {
		timeAvailW = box.width / 2
		periodWidth = max(periodSize*2, box.width-gap-timeAvailW)
	}

	// Largest face that fits both the tall clock band and the available width.
	// Instrument Serif digits are roughly 0.44em wide; keep a small pad so the
	// last digit is not cut while allowing the face to grow into the wide band.
	fontByHeight := box.height * 98 / 100
	fontByWidth := timeAvailW * 100 / (44*glyphs + 8)
	fontSize := options.FontSize
	if fontSize > fontByHeight {
		fontSize = fontByHeight
	}
	if fontSize > fontByWidth {
		fontSize = fontByWidth
	}
	if fontSize < 1 {
		fontSize = 1
	}

	timeWidth := min(timeAvailW, fontSize*44*glyphs/100+fontSize*12/100)
	// Center the time itself—not the time-plus-period group—so the dominant
	// digits always sit on the display axis, including during 10–12 o'clock.
	timeLeft := box.left + (box.width-timeWidth)/2
	periodLeft := timeLeft + timeWidth + gap
	if maxPeriodLeft := box.left + box.width - periodWidth; periodLeft > maxPeriodLeft {
		periodLeft = maxPeriodLeft
	}

	timeBox := displayRect{
		top: box.top, left: timeLeft,
		width: timeWidth, height: box.height,
	}
	periodBox := displayRect{
		top:    box.top + box.height*22/100,
		left:   periodLeft,
		width:  periodWidth,
		height: box.height * 42 / 100,
	}
	// padding=NONE lets px fill nearly the full type region height.
	timeSpec := typeSpecNoPad(clockTimeFont, fontSize, timeBox, screenWidth, screenHeight)
	periodSpec := typeSpecNoPad(options.FontPath, periodSize, periodBox, screenWidth, screenHeight)

	// Both writes are framebuffer-only. The final command refreshes exactly the
	// clock rectangle, preventing intermediate white flashes and screen-wide wear.
	command := fmt.Sprintf(
		`%s -q -b -B WHITE -k %s && %s -q -b -B WHITE -C BLACK -m -M -t %s -- %s && %s -q -b -B WHITE -C BLACK -m -M -t %s -- %s && %s -q -W GC16 -s %s`,
		fbinkPath, region,
		fbinkPath, shellQuote(timeSpec), shellQuote(timeText),
		fbinkPath, shellQuote(periodSpec), shellQuote(periodText),
		fbinkPath, region,
	)
	if _, err := d.run(ctx, command); err != nil {
		return fmt.Errorf("clock: draw %q: %w", now.Format("3:04 PM"), err)
	}
	return nil
}

func (d *Device) drawDate(value string, options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
	return d.drawDateContext(context.Background(), value, options, layout, screenWidth, screenHeight)
}

func (d *Device) drawDateContext(ctx context.Context, value string, options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
	// Target ~9% of screen height (capped to the date band).
	return d.drawTextRegionContext(ctx, "date", value, options.FontPath, screenHeight*9/100, layout.date, screenWidth, screenHeight, true)
}

func (d *Device) drawTextRegion(name, value, font string, fontSize int, box displayRect, screenWidth, screenHeight int, centered bool) error {
	return d.drawTextRegionContext(context.Background(), name, value, font, fontSize, box, screenWidth, screenHeight, centered)
}

func (d *Device) drawTextRegionContext(ctx context.Context, name, value, font string, fontSize int, box displayRect, screenWidth, screenHeight int, centered bool) error {
	if box.width < 2 || box.height < 2 {
		return fmt.Errorf("clock: draw %s: region too small (%dx%d)", name, box.width, box.height)
	}
	// Cap font to the box so FBInk does not silently skip oversized type.
	// padding=NONE lets faces fill nearly the full region height.
	if fontSize > box.height*96/100 {
		fontSize = max(1, box.height*96/100)
	}
	centerFlag := ""
	if centered {
		centerFlag = " -m -M"
	}
	region := regionSpec(box)
	command := fmt.Sprintf(`%s -q -b -B WHITE -k %s && %s -q -b -B WHITE -C BLACK%s -t %s -- %s && %s -q -W GC16 -s %s`,
		fbinkPath, region, fbinkPath, centerFlag, shellQuote(typeSpecNoPad(font, fontSize, box, screenWidth, screenHeight)), shellQuote(value), fbinkPath, region)
	if _, err := d.run(ctx, command); err != nil {
		return fmt.Errorf("clock: draw %s: %w", name, err)
	}
	return nil
}

// fillRect paints a solid rectangle. black=true fills with black (rules/dividers);
// black=false clears to white.
func (d *Device) fillRect(box displayRect, black bool) error {
	return d.fillRectContext(context.Background(), box, black)
}

func (d *Device) fillRectContext(ctx context.Context, box displayRect, black bool) error {
	// FBInk treats 1px regions as empty and refuses to refresh them.
	if box.width < 2 || box.height < 2 {
		return nil
	}
	bg := "WHITE"
	if black {
		bg = "BLACK"
	}
	region := regionSpec(box)
	// Fill via clear-with-background, then refresh the same region so the
	// line is visible without a full-screen flash.
	command := fmt.Sprintf(`%s -q -b -B %s -k %s && %s -q -W GC16 -s %s`,
		fbinkPath, bg, region, fbinkPath, region)
	if _, err := d.run(ctx, command); err != nil {
		return err
	}
	return nil
}

// drawDottedLine approximates the mockup's dotted full-width separator by
// stamping short black segments with white gaps between them.
func (d *Device) drawDottedLine(box displayRect) error {
	return d.drawDottedLineContext(context.Background(), box)
}

func (d *Device) drawDottedLineContext(ctx context.Context, box displayRect) error {
	if box.width < 2 || box.height < 2 {
		return nil
	}
	// Clear the strip first so old ink doesn't ghost under the dots.
	if err := d.fillRectContext(ctx, box, false); err != nil {
		return err
	}
	dash := max(3, box.width*6/1000)
	gap := max(3, box.width*5/1000)
	var parts []string
	for left := box.left; left+dash <= box.left+box.width; left += dash + gap {
		seg := displayRect{top: box.top, left: left, width: dash, height: max(2, box.height)}
		parts = append(parts, fmt.Sprintf(`%s -q -b -B BLACK -k %s`, fbinkPath, regionSpec(seg)))
	}
	if len(parts) == 0 {
		return nil
	}
	// One regional refresh over the whole strip after all dashes are painted.
	parts = append(parts, fmt.Sprintf(`%s -q -W GC16 -s %s`, fbinkPath, regionSpec(box)))
	if _, err := d.run(ctx, strings.Join(parts, " && ")); err != nil {
		return err
	}
	return nil
}

func typeSpec(font string, fontSize int, box displayRect, screenWidth, screenHeight int) string {
	return typeSpecPad(font, fontSize, box, screenWidth, screenHeight, "BOTH")
}

// typeSpecNoPad maximizes face size inside a region (used for the main clock digits).
func typeSpecNoPad(font string, fontSize int, box displayRect, screenWidth, screenHeight int) string {
	return typeSpecPad(font, fontSize, box, screenWidth, screenHeight, "NONE")
}

func typeSpecPad(font string, fontSize int, box displayRect, screenWidth, screenHeight int, padding string) string {
	right := screenWidth - box.left - box.width
	bottom := screenHeight - box.top - box.height
	return fmt.Sprintf("regular=%s,px=%d,top=%d,bottom=%d,left=%d,right=%d,padding=%s",
		font, fontSize, box.top, bottom, box.left, right, padding)
}

func regionSpec(box displayRect) string {
	return fmt.Sprintf("top=%d,left=%d,width=%d,height=%d", box.top, box.left, box.width, box.height)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
