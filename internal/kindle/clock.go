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
const clockTimeFont = "/mnt/us/fonts/InstrumentSerif-Regular.ttf"

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

	layout := newDashboardLayout(width, height)
	// Prefer the largest face that still fits the clock band. drawClock may
	// shrink slightly for long times ("12:59") so AM/PM stays visible.
	if options.FontSize == 0 {
		options.FontSize = layout.clock.height * 98 / 100
	}
	if options.FontSize < 1 {
		return fmt.Errorf("clock: font size must be positive")
	}
	maxFont := layout.clock.height * 98 / 100
	if options.FontSize > maxFont {
		options.FontSize = maxFont
	}

	if err := d.drawStaticChrome(options, layout, width, height); err != nil {
		return err
	}
	lastDate := ""
	lastClock := ""

	for {
		now := options.Now()
		date := now.Format("Monday, January 2")
		if date != lastDate {
			if err := d.drawDate(date, options, layout, width, height); err != nil {
				return err
			}
			lastDate = date
		}
		clock := now.Format("3:04 PM")
		if clock != lastClock {
			if err := d.drawClock(now, options, layout, width, height); err != nil {
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

// ShowClockOnce paints the clock dashboard once and returns. It reuses the
// same layout and rendering path as RunClock, while cancelling an internal
// child context so RunClock exits after its first frame instead of waiting for
// the next minute boundary.
func (d *Device) ShowClockOnce(ctx context.Context, options ClockOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	onceCtx, cancel := context.WithCancel(ctx)
	cancel()
	return d.RunClock(onceCtx, options)
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

	// Metrics strip is tall enough for large reading digits under compact labels,
	// while preserving a little breathing room at the bottom of the display.
	metricsTop := height * 73 / 100
	metricsH := height * 26 / 100
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
	climateTop := height * 66 / 100
	climateH := height * 6 / 100
	return dashboardLayout{
		// Let the time dominate the display while retaining a slim bezel margin.
		clock: displayRect{
			top: height * 0 / 100, left: clockMargin,
			width: width - 2*clockMargin, height: height * 59 / 100,
		},
		clockRule: displayRect{
			top: height * 59 / 100, left: (width - ruleW) / 2,
			width: ruleW, height: line,
		},
		date: displayRect{
			top: height * 60 / 100, left: marginX,
			width: contentW, height: height * 5 / 100,
		},
		divider: displayRect{
			top: height * 65 / 100, left: marginX,
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

const fbinkPath = "/mnt/us/usbnet/bin/fbink"

// drawStaticChrome paints the non-clock chrome once at startup: rules,
// climate row, metric labels/placeholders, and column dividers.
func (d *Device) drawStaticChrome(options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
	if err := d.fillRect(layout.clockRule, true); err != nil {
		return fmt.Errorf("clock: clock rule: %w", err)
	}
	if err := d.drawDottedLine(layout.divider); err != nil {
		return fmt.Errorf("clock: divider: %w", err)
	}
	// Keep label and value together so the row reads as one centered status.
	if err := d.drawTextRegion("climate", "Climate: Warm", options.FontPath, screenHeight*7/100, layout.climate, screenWidth, screenHeight, true); err != nil {
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
		{"temperature", "TEMP", "24", "°C"},
		{"pressure", "PRESS", "1013", "hPa"},
		{"humidity", "HUMID", "58", "%"},
		{"pm25", "PM2.5", "12", "µg/m³"},
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
		if err := d.drawTextRegion(metric.name+" label", metric.label, options.FontPath, labelSize, labelBox, screenWidth, screenHeight, true); err != nil {
			return err
		}
		if err := d.fillRect(layout.metricRules[i], true); err != nil {
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
		if err := d.drawTextRegion(metric.name+" number", metric.number, options.FontPath, numberSize, numberBox, screenWidth, screenHeight, true); err != nil {
			return err
		}
		unitBox := displayRect{
			top: box.top + box.height*76/100, left: box.left,
			width: box.width, height: box.height * 18 / 100,
		}
		if err := d.drawTextRegion(metric.name+" unit", metric.unit, options.FontPath, unitSize, unitBox, screenWidth, screenHeight, true); err != nil {
			return err
		}
	}
	for i, div := range layout.metricDividers {
		if err := d.fillRect(div, true); err != nil {
			return fmt.Errorf("clock: metric divider %d: %w", i, err)
		}
	}
	return nil
}

func (d *Device) drawClock(now time.Time, options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
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
	if _, err := d.client.Run(command); err != nil {
		return fmt.Errorf("clock: draw %q: %w", now.Format("3:04 PM"), err)
	}
	return nil
}

func (d *Device) drawDate(value string, options ClockOptions, layout dashboardLayout, screenWidth, screenHeight int) error {
	return d.drawTextRegion("date", value, options.FontPath, screenHeight*6/100, layout.date, screenWidth, screenHeight, true)
}

func (d *Device) drawTextRegion(name, value, font string, fontSize int, box displayRect, screenWidth, screenHeight int, centered bool) error {
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
	if _, err := d.client.Run(command); err != nil {
		return fmt.Errorf("clock: draw %s: %w", name, err)
	}
	return nil
}

// fillRect paints a solid rectangle. black=true fills with black (rules/dividers);
// black=false clears to white.
func (d *Device) fillRect(box displayRect, black bool) error {
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
	if _, err := d.client.Run(command); err != nil {
		return err
	}
	return nil
}

// drawDottedLine approximates the mockup's dotted full-width separator by
// stamping short black segments with white gaps between them.
func (d *Device) drawDottedLine(box displayRect) error {
	if box.width < 2 || box.height < 2 {
		return nil
	}
	// Clear the strip first so old ink doesn't ghost under the dots.
	if err := d.fillRect(box, false); err != nil {
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
	if _, err := d.client.Run(strings.Join(parts, " && ")); err != nil {
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
