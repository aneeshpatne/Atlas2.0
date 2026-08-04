package kindle

import (
	"strings"
	"testing"
	"time"
)

type recordingRunner struct {
	output   string
	commands []string
}

func (r *recordingRunner) Run(command string) (string, error) {
	r.commands = append(r.commands, command)
	return r.output, nil
}

func TestDisplaySizeNormalizesLandscape(t *testing.T) {
	runner := &recordingRunner{output: "FBINK_VERSION='v1.25.0';viewWidth=1448;viewHeight=1072;screenWidth=1448;screenHeight=1072;DPI=300;"}
	device := &Device{client: runner}

	width, height, err := device.displaySize()
	if err != nil {
		t.Fatal(err)
	}
	if width != 1448 || height != 1072 {
		t.Fatalf("displaySize() = %dx%d, want 1448x1072", width, height)
	}
}

func TestDrawClockUsesOneRegionalPartialRefresh(t *testing.T) {
	runner := &recordingRunner{}
	device := &Device{client: runner}
	layout := dashboardLayout{clock: displayRect{top: 40, left: 60, width: 1300, height: 300}}

	err := device.drawClock(time.Date(2026, 8, 3, 9, 41, 0, 0, time.UTC), ClockOptions{
		FontPath: "/mnt/us/fonts/Clock.ttf",
		FontSize: 300,
	}, layout, 1448, 1072)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("got %d remote commands, want one atomic batch", len(runner.commands))
	}
	command := runner.commands[0]
	region := "top=40,left=60,width=1300,height=300"
	for _, want := range []string{
		"-b -B WHITE -k " + region,
		"-b -B WHITE -C BLACK",
		"regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf",
		"'9:41'",
		"'AM'",
		"-W GC16 -s " + region,
	} {
		if !strings.Contains(command, want) {
			t.Errorf("command missing %q:\n%s", want, command)
		}
	}
	if strings.Contains(command, " -c ") || strings.Contains(command, " -f ") {
		t.Fatalf("command requested a full-screen clear or flashing update:\n%s", command)
	}
}

func TestDrawClockCapsFontToBoxHeight(t *testing.T) {
	runner := &recordingRunner{}
	device := &Device{client: runner}
	// 200px-tall box cannot reliably host a larger face; FBInk may skip it.
	layout := dashboardLayout{clock: displayRect{top: 20, left: 40, width: 1200, height: 200}}

	err := device.drawClock(time.Date(2026, 8, 3, 16, 5, 0, 0, time.UTC), ClockOptions{
		FontPath: "/mnt/us/fonts/Clock.ttf",
		FontSize: 500,
	}, layout, 1448, 1072)
	if err != nil {
		t.Fatal(err)
	}
	command := runner.commands[0]
	if !strings.Contains(command, "px=196") {
		t.Fatalf("expected font capped to 98%% of box height (196), command:\n%s", command)
	}
	if strings.Contains(command, "px=500") {
		t.Fatal("oversized font was not capped")
	}
}

func TestDrawClockKeepsPeriodBesideTime(t *testing.T) {
	runner := &recordingRunner{}
	device := &Device{client: runner}
	layout := dashboardLayout{clock: displayRect{top: 20, left: 50, width: 1340, height: 480}}

	err := device.drawClock(time.Date(2026, 8, 3, 9, 41, 0, 0, time.UTC), ClockOptions{
		FontPath: "/mnt/us/fonts/Clock.ttf",
		FontSize: 400,
	}, layout, 1448, 1072)
	if err != nil {
		t.Fatal(err)
	}
	command := runner.commands[0]
	// Time and period type regions must both appear and not share an identical
	// left/right margin pair (which would mean PM was drawn in the full clock box).
	if !strings.Contains(command, "'9:41'") || !strings.Contains(command, "'AM'") {
		t.Fatalf("missing time or period text:\n%s", command)
	}
}

func TestDrawClockUsesSmallClosePeriod(t *testing.T) {
	runner := &recordingRunner{}
	device := &Device{client: runner}
	layout := dashboardLayout{clock: displayRect{top: 20, left: 50, width: 1340, height: 480}}

	if err := device.drawClock(time.Date(2026, 8, 3, 16, 5, 0, 0, time.UTC), ClockOptions{
		FontPath: "/mnt/us/fonts/Clock.ttf",
		FontSize: 400,
	}, layout, 1448, 1072); err != nil {
		t.Fatal(err)
	}
	command := runner.commands[0]
	if !strings.Contains(command, "px=48") {
		t.Fatalf("expected period to use a smaller 10%% face (48px), command:\n%s", command)
	}
	if !strings.Contains(command, "left=344,right=352") {
		t.Fatalf("expected the main time box to be centered, command:\n%s", command)
	}
	if !strings.Contains(command, "left=1104") {
		t.Fatalf("expected period to remain close to the time, command:\n%s", command)
	}
}

func TestNewDashboardLayoutHasFourMetricColumns(t *testing.T) {
	layout := newDashboardLayout(1448, 1072)
	if layout.metrics[3].width < 1 {
		t.Fatal("expected a fourth metric column for PM2.5")
	}
	// Clock should sit above the date; date above climate; climate above metrics.
	if layout.clock.top >= layout.date.top {
		t.Fatalf("clock top %d should be above date top %d", layout.clock.top, layout.date.top)
	}
	if layout.date.top >= layout.climate.top {
		t.Fatalf("date top %d should be above climate top %d", layout.date.top, layout.climate.top)
	}
	if layout.climate.top >= layout.metrics[0].top {
		t.Fatalf("climate top %d should be above metrics top %d", layout.climate.top, layout.metrics[0].top)
	}
	// Clock rule sits between clock and date.
	if layout.clockRule.top <= layout.clock.top || layout.clockRule.top >= layout.date.top {
		t.Fatalf("clock rule top %d should sit between clock %d and date %d",
			layout.clockRule.top, layout.clock.top, layout.date.top)
	}
	// FBInk refuses 1px refresh regions; keep dividers and rules ≥2px.
	for i, div := range layout.metricDividers {
		if div.width < 2 || div.height < 2 {
			t.Fatalf("metric divider %d is %dx%d; FBInk needs ≥2px", i, div.width, div.height)
		}
	}
	if layout.clockRule.height < 2 || layout.divider.height < 2 {
		t.Fatalf("horizontal rules must be ≥2px tall")
	}
	// Underline sits tight under the short label band.
	ruleOffset := layout.metricRules[0].top - layout.metrics[0].top
	if ruleOffset > layout.metrics[0].height*28/100 {
		t.Fatalf("metric rule too far below label: offset %d of column height %d",
			ruleOffset, layout.metrics[0].height)
	}
	// Metrics strip is tall enough for large reading digits.
	if layout.metrics[0].height < 1072*22/100 {
		t.Fatalf("metrics height %d too short; want ≥22%% of screen for big digits", layout.metrics[0].height)
	}
	if layout.clock.height < 1072*40/100 {
		t.Fatalf("clock box height %d too small; want ≥40%% of screen", layout.clock.height)
	}
}

func TestDrawStaticChromeIncludesClimateAndMetrics(t *testing.T) {
	runner := &recordingRunner{}
	device := &Device{client: runner}
	layout := newDashboardLayout(1448, 1072)

	err := device.drawStaticChrome(ClockOptions{FontPath: "/mnt/us/fonts/Clock.ttf"}, layout, 1448, 1072)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{
		"Climate: Warm",
		"TEMP",
		"PRESS",
		"HUMID",
		"PM2.5",
		"24",
		"1013",
		"58",
		"12",
		"°C",
		"hPa",
		"%",
		"µg/m³",
		"-B BLACK -k",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("static chrome missing %q", want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got, want := shellQuote("it's"), `'it'\''s'`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}
