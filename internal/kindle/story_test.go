package kindle

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

type imageRecordingRunner struct {
	recordingRunner
	uploadedPath string
	uploadedData []byte
}

func (r *imageRecordingRunner) Upload(path string, data []byte) error {
	r.uploadedPath = path
	r.uploadedData = append([]byte(nil), data...)
	return nil
}

func TestRunStoryCleansScreenOnStartAndDrawsPayload(t *testing.T) {
	runner := &recordingRunner{output: "screenWidth=1448;screenHeight=1072"}
	device := &Device{client: runner}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := device.RunStory(ctx, Story{
		Title:       "Lok Sabha clears Supreme Court judge bill",
		Description: "Bill raises sanctioned Supreme Court strength from 34 to 38, including the CJI.",
		Genre:       "India",
		Sources:     []StorySource{{Domain: "livelaw"}},
	}, StoryOptions{FontPath: "/mnt/us/fonts/Helvetica.ttf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) < 4 {
		t.Fatalf("got %d commands, want startup commands and story drawing", len(runner.commands))
	}
	for i, want := range []string{
		"echo 0 > /sys/class/graphics/fb0/rotate",
		"/mnt/us/usbnet/bin/fbink -q -c -f -W GC16",
		"lipc-set-prop com.lab126.powerd flIntensity 20",
		"/mnt/us/usbnet/bin/fbink -e",
	} {
		if runner.commands[i] != want {
			t.Errorf("startup command %d = %q, want %q", i, runner.commands[i], want)
		}
	}
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{
		"INDIA",
		"Lok Sabha clears Supreme Court judge bill",
		"Bill raises sanctioned Supreme Court strength from 34 to 38, including the CJI.",
		"SOURCE  ·  livelaw",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("story screen missing %q", want)
		}
	}
}

func TestRunStoryRequiresTitleBeforeChangingDevice(t *testing.T) {
	runner := &recordingRunner{}
	err := (&Device{client: runner}).RunStory(context.Background(), Story{}, StoryOptions{})
	if err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Fatalf("RunStory() error = %v, want missing-title error", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("invalid story changed device with %d commands", len(runner.commands))
	}
}

func TestStorySourceLabelDeduplicatesDomains(t *testing.T) {
	got := storySourceLabel([]StorySource{{Domain: "livelaw"}, {Domain: "livelaw"}, {Domain: "barandbench"}})
	if want := "SOURCE  ·  livelaw  /  barandbench"; got != want {
		t.Fatalf("storySourceLabel() = %q, want %q", got, want)
	}
}

func TestRunStoryUsesGenreFallbackWhenNoOGURL(t *testing.T) {
	runner := &imageRecordingRunner{recordingRunner: recordingRunner{output: "screenWidth=1448;screenHeight=1072"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var fallback bytes.Buffer
	bg := image.NewGray(image.Rect(0, 0, 2, 2))
	bg.SetGray(0, 0, color.Gray{Y: 180})
	if err := png.Encode(&fallback, bg); err != nil {
		t.Fatal(err)
	}

	err := (&Device{client: runner}).RunStory(ctx, Story{
		Title:   "No og image",
		Genre:   "India",
		Sources: []StorySource{{Domain: "example"}},
	}, StoryOptions{FallbackImage: fallback.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if runner.uploadedPath != remoteStoryImage || len(runner.uploadedData) == 0 {
		t.Fatalf("expected genre fallback upload, got (%q, %d bytes)", runner.uploadedPath, len(runner.uploadedData))
	}
	joined := strings.Join(runner.commands, "\n")
	if !strings.Contains(joined, "-q -w -W GC16 -i '/tmp/atlas-story-image'") {
		t.Fatalf("expected fallback background draw:\n%s", joined)
	}
}

func TestRunStoryDownloadsUploadsAndRendersOGImage(t *testing.T) {
	runner := &imageRecordingRunner{recordingRunner: recordingRunner{output: "screenWidth=1448;screenHeight=1072"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := (&Device{client: runner}).RunStory(ctx, Story{
		Title: "A story with an image",
		Sources: []StorySource{{
			Domain: "example",
			OGURL:  "https://example.com/story.jpg",
		}},
	}, StoryOptions{
		FetchImage: func(_ context.Context, url string) ([]byte, error) {
			if url != "https://example.com/story.jpg" {
				t.Fatalf("FetchImage URL = %q", url)
			}
			var imageData bytes.Buffer
			background := image.NewGray(image.Rect(0, 0, 2, 2))
			background.SetGray(0, 0, color.Gray{Y: 220})
			if err := png.Encode(&imageData, background); err != nil {
				t.Fatal(err)
			}
			return imageData.Bytes(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.uploadedPath != remoteStoryImage || len(runner.uploadedData) == 0 {
		t.Fatalf("upload = (%q, %d bytes)", runner.uploadedPath, len(runner.uploadedData))
	}
	joined := strings.Join(runner.commands, "\n")
	for _, want := range []string{
		"-q -w -W GC16 -i '/tmp/atlas-story-image'",
		"x=0,y=0,w=1448,h=1072,dither",
		"rm -f /tmp/atlas-story-image",
		"-q -w -W GC16 -O -C WHITE -B BLACK",
		"left=101",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("image story commands missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-m -M") {
		t.Fatal("overlay text should use absolute region positioning")
	}
}

func TestStripURLQuery(t *testing.T) {
	got := stripURLQuery("https://c.ndtvimg.com/a.png?im=FeatureCrop,width=1600")
	if want := "https://c.ndtvimg.com/a.png"; got != want {
		t.Fatalf("stripURLQuery() = %q, want %q", got, want)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	if !looksLikeHTML([]byte("<HTML><HEAD><TITLE>Access Denied</TITLE>")) {
		t.Fatal("expected HTML detection")
	}
	if looksLikeHTML([]byte{0x89, 'P', 'N', 'G'}) {
		t.Fatal("PNG signature should not look like HTML")
	}
}

func TestPrepareStoryBackgroundDarkensAndFitsImage(t *testing.T) {
	var input bytes.Buffer
	source := image.NewGray(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			source.SetGray(x, y, color.Gray{Y: 200})
		}
	}
	if err := png.Encode(&input, source); err != nil {
		t.Fatal(err)
	}
	output, err := prepareStoryBackground(input.Bytes(), 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(output))
	if err != nil {
		t.Fatal(err)
	}
	// y=0 → scrim 0; highlight roll-off: 200 → 150+(50*65/100)=182
	// then *70/100 = 127; * (100-0)/100 = 127
	gray := color.GrayModel.Convert(decoded.At(0, 0)).(color.Gray)
	if gray.Y != 127 {
		t.Fatalf("darkened pixel = %d, want 127", gray.Y)
	}
	// Mid-frame should apply the copy-band scrim and be darker still.
	mid := color.GrayModel.Convert(decoded.At(0, 1)).(color.Gray)
	if mid.Y >= gray.Y {
		t.Fatalf("mid-frame pixel %d should be darker than top-edge %d", mid.Y, gray.Y)
	}
}

func TestStoryScrimPercentPeaksInCopyBand(t *testing.T) {
	if storyScrimPercent(0) != 0 {
		t.Fatalf("top edge scrim = %d, want 0", storyScrimPercent(0))
	}
	if storyScrimPercent(50) != storyScrimPeakPercent {
		t.Fatalf("mid scrim = %d, want %d", storyScrimPercent(50), storyScrimPeakPercent)
	}
	if storyScrimPercent(99) >= storyScrimPercent(50) {
		t.Fatalf("bottom scrim should ease off from mid")
	}
}
