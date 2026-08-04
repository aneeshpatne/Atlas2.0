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
		"-q -w -W GC16 -O -C WHITE",
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

func TestPrepareStoryBackgroundDarkensAndFitsImage(t *testing.T) {
	var input bytes.Buffer
	source := image.NewGray(image.Rect(0, 0, 2, 2))
	source.SetGray(0, 0, color.Gray{Y: 200})
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
	gray := color.GrayModel.Convert(decoded.At(0, 0)).(color.Gray)
	if gray.Y != 110 {
		t.Fatalf("darkened pixel = %d, want 110", gray.Y)
	}
}
