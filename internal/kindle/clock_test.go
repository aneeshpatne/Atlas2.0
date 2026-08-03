package kindle

import (
	"strings"
	"testing"
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
	box := displayRect{top: 250, left: 0, width: 1680, height: 700}

	err := device.drawClock("09:41", ClockOptions{
		FontPath: "/mnt/us/fonts/Clock.ttf",
		FontSize: 500,
	}, box, 1264)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("got %d remote commands, want one atomic batch", len(runner.commands))
	}
	command := runner.commands[0]
	region := "top=250,left=0,width=1680,height=700"
	for _, want := range []string{
		"-b -B WHITE -k " + region,
		"-b -B WHITE -C BLACK",
		"'09:41'",
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

func TestShellQuote(t *testing.T) {
	if got, want := shellQuote("it's"), `'it'\''s'`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}
