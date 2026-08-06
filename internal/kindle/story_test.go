package kindle

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

type imageRecordingRunner struct {
	recordingRunner
	uploadedPath string
	uploadedData []byte
	cancel       context.CancelFunc
}

func (r *imageRecordingRunner) Run(command string) (string, error) {
	output, err := r.recordingRunner.Run(command)
	if r.cancel != nil && strings.Contains(command, "-q -W GC16 -s top=0") {
		r.cancel()
	}
	return output, err
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
	time.AfterFunc(250*time.Millisecond, cancel)

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

func TestRunStoryHonorsAlreadyCancelledContext(t *testing.T) {
	runner := &recordingRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&Device{client: runner}).RunStory(ctx, Story{Title: "cancelled"}, StoryOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunStory error = %v, want context cancellation", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("cancelled story issued commands: %v", runner.commands)
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
	runner.cancel = cancel

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
	runner.cancel = cancel

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
		"rm -f '/tmp/atlas-story-image'",
		"-q -b -O -C WHITE -B BLACK",
		"-q -W GC16 -s top=0,left=0,width=1448,height=1072",
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

func TestRunStoryCachesPreparedOGImage(t *testing.T) {
	runner := &imageRecordingRunner{recordingRunner: recordingRunner{output: "screenWidth=100;screenHeight=80"}}
	device := &Device{client: runner}
	fetches := 0
	options := StoryOptions{FetchImage: func(_ context.Context, _ string) ([]byte, error) {
		fetches++
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
			t.Fatal(err)
		}
		return encoded.Bytes(), nil
	}}
	story := Story{Title: "Repeated story", Sources: []StorySource{{OGURL: "https://example.com/repeated.jpg"}}}

	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		runner.cancel = cancel
		if err := device.RunStory(ctx, story, options); err != nil {
			t.Fatal(err)
		}
	}
	if fetches != 1 {
		t.Fatalf("image fetches = %d, want 1", fetches)
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

func TestValidatePublicImageURLRejectsLocalAddresses(t *testing.T) {
	for _, raw := range []string{"http://example.com/image.jpg", "https://127.0.0.1/image.jpg", "https://[::1]/image.jpg", "https://user:pass@example.com/image.jpg"} {
		if err := validatePublicImageURL(context.Background(), raw); err == nil {
			t.Errorf("validatePublicImageURL(%q) unexpectedly succeeded", raw)
		}
	}
	if err := validateImageURL(context.Background(), "https://127.0.0.1/image.jpg", true); err != nil {
		t.Fatalf("explicit private-image policy rejected local URL: %v", err)
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
	// A flat image uses the full source range but is still bounded for e-ink.
	gray := color.GrayModel.Convert(decoded.At(0, 0)).(color.Gray)
	if gray.Y <= storyShadowFloor || gray.Y >= storyHighlightCeiling {
		t.Fatalf("tone-mapped pixel = %d, want inside (%d, %d)", gray.Y, storyShadowFloor, storyHighlightCeiling)
	}
	// Mid-frame should apply the copy-band scrim and be darker still.
	mid := color.GrayModel.Convert(decoded.At(0, 1)).(color.Gray)
	if mid.Y >= gray.Y {
		t.Fatalf("mid-frame pixel %d should be darker than top-edge %d", mid.Y, gray.Y)
	}
}

func TestEinkTonePreservesShadowDetailAndCapsHighlights(t *testing.T) {
	if got := einkTone(0, 0, 255); got != storyShadowFloor {
		t.Fatalf("black = %d, want shadow floor %d", got, storyShadowFloor)
	}
	if got := einkTone(255, 0, 255); got != storyHighlightCeiling {
		t.Fatalf("white = %d, want highlight ceiling %d", got, storyHighlightCeiling)
	}
	shadow, midtone := einkTone(20, 0, 255), einkTone(100, 0, 255)
	if shadow <= storyShadowFloor || midtone <= shadow {
		t.Fatalf("tone curve lost detail: floor=%d shadow=%d midtone=%d", storyShadowFloor, shadow, midtone)
	}
}

func TestStoryScrimLeavesWhiteTextContrast(t *testing.T) {
	copyBandPeak := einkTone(255, 0, 255) * (100 - storyScrimPeakPercent) / 100
	if copyBandPeak > 110 {
		t.Fatalf("copy-band background peak = %d, want <= 110 for white text", copyBandPeak)
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
