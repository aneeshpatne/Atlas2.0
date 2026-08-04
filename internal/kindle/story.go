package kindle

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"
)

const remoteStoryImage = "/tmp/atlas-story-image"

// Story is the news event shape rendered by story mode.
type Story struct {
	StoryID     string        `json:"storyId"`
	EventID     string        `json:"eventId"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Genre       string        `json:"genre"`
	Sources     []StorySource `json:"sources"`
}

type StorySource struct {
	URL    string `json:"url"`
	Domain string `json:"domain"`
	OGURL  string `json:"ogurl"`
}

type StoryOptions struct {
	FontPath   string
	FetchImage func(context.Context, string) ([]byte, error)
}

type storyLayout struct {
	image, genre, genreRule, title, description, source displayRect
}

func newStoryLayout(width, height int, withImage bool) storyLayout {
	margin := width * 7 / 100
	contentLeft := margin
	contentWidth := width - 2*margin
	image := displayRect{}
	if withImage {
		image = displayRect{top: 0, left: 0, width: width, height: height}
	}
	line := max(2, height*2/1000)
	return storyLayout{
		image:       image,
		genre:       displayRect{top: height * 7 / 100, left: contentLeft, width: contentWidth, height: height * 7 / 100},
		genreRule:   displayRect{top: height * 16 / 100, left: contentLeft, width: min(width*13/100, contentWidth), height: line},
		title:       displayRect{top: height * 20 / 100, left: contentLeft, width: contentWidth, height: height * 40 / 100},
		description: displayRect{top: height * 63 / 100, left: contentLeft, width: contentWidth, height: height * 22 / 100},
		source:      displayRect{top: height * 89 / 100, left: contentLeft, width: contentWidth, height: height * 5 / 100},
	}
}

// RunStory performs the same startup cleanup as RunClock, then paints one
// static editorial story screen and waits until the process is stopped.
func (d *Device) RunStory(ctx context.Context, story Story, options StoryOptions) error {
	if strings.TrimSpace(story.Title) == "" {
		return fmt.Errorf("story: title is required")
	}
	if options.FontPath == "" {
		options.FontPath = defaultClockFont
	}
	if options.FetchImage == nil {
		options.FetchImage = fetchStoryImage
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
	imageReady := false
	if imageURL := firstStoryImageURL(story.Sources); imageURL != "" {
		if uploader, ok := d.client.(interface{ Upload(string, []byte) error }); ok {
			if data, err := options.FetchImage(ctx, imageURL); err == nil {
				if data, err = prepareStoryBackground(data, width, height); err == nil {
					imageReady = uploader.Upload(remoteStoryImage, data) == nil
				}
			}
		}
	}
	layout := newStoryLayout(width, height, imageReady)
	if imageReady {
		if err := d.drawStoryImage(layout.image); err != nil {
			layout = newStoryLayout(width, height, false)
		}
		_, _ = d.client.Run("rm -f " + remoteStoryImage)
	}
	if err := d.drawStory(story, options, layout, width, height); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (d *Device) drawStoryImage(box displayRect) error {
	spec := fmt.Sprintf("x=%d,y=%d,w=%d,h=%d,dither", box.left, box.top, box.width, box.height)
	// Complete the background refresh before any overlay text is drawn.
	command := fmt.Sprintf("%s -q -w -W GC16 -i %s -g %s", fbinkPath, shellQuote(remoteStoryImage), shellQuote(spec))
	if _, err := d.client.Run(command); err != nil {
		return fmt.Errorf("story: draw image: %w", err)
	}
	return nil
}

func firstStoryImageURL(sources []StorySource) string {
	for _, source := range sources {
		if value := strings.TrimSpace(source.OGURL); value != "" {
			return value
		}
	}
	return ""
}

func fetchStoryImage(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("image request returned %s", response.Status)
	}
	const maxImageBytes = 12 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return nil, fmt.Errorf("image size must be between 1 byte and 12 MiB")
	}
	return data, nil
}

// prepareStoryBackground turns a source image into a display-sized, grayscale
// PNG with its luminance reduced enough for white overlay text to remain clear.
func prepareStoryBackground(data []byte, width, height int) ([]byte, error) {
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	srcBounds := source.Bounds()
	if srcBounds.Dx() < 1 || srcBounds.Dy() < 1 || srcBounds.Dx() > 10000 || srcBounds.Dy() > 10000 {
		return nil, fmt.Errorf("image dimensions are not supported")
	}
	if width < 1 || height < 1 {
		return nil, fmt.Errorf("invalid display dimensions %dx%d", width, height)
	}

	// Use a cover crop so there are no white bars behind the overlaid copy.
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()
	if srcW*height > width*srcH {
		cropW := srcH * width / height
		srcBounds.Min.X += (srcW - cropW) / 2
		srcBounds.Max.X = srcBounds.Min.X + cropW
	} else {
		cropH := srcW * height / width
		srcBounds.Min.Y += (srcH - cropH) / 2
		srcBounds.Max.Y = srcBounds.Min.Y + cropH
	}

	background := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			srcX := srcBounds.Min.X + x*srcBounds.Dx()/width
			srcY := srcBounds.Min.Y + y*srcBounds.Dy()/height
			gray := color.GrayModel.Convert(source.At(srcX, srcY)).(color.Gray)
			// Retain detail while making the background substantially darker.
			background.SetGray(x, y, color.Gray{Y: uint8(int(gray.Y) * 55 / 100)})
		}
	}

	var output bytes.Buffer
	if err := png.Encode(&output, background); err != nil {
		return nil, fmt.Errorf("encode background: %w", err)
	}
	return output.Bytes(), nil
}

func (d *Device) drawStory(story Story, options StoryOptions, layout storyLayout, width, height int) error {
	if layout.image.width >= 2 && layout.image.height >= 2 {
		return d.drawStoryOverlay(story, options, layout, width, height)
	}
	genre := strings.ToUpper(strings.TrimSpace(story.Genre))
	if genre == "" {
		genre = "NEWS"
	}
	if err := d.drawTextRegion("story genre", genre, options.FontPath, height*4/100, layout.genre, width, height, false); err != nil {
		return err
	}
	if err := d.fillRect(layout.genreRule, true); err != nil {
		return fmt.Errorf("story: genre rule: %w", err)
	}
	if err := d.drawTextRegion("story title", story.Title, clockTimeFont, height*15/100, layout.title, width, height, false); err != nil {
		return err
	}
	if description := strings.TrimSpace(story.Description); description != "" {
		if err := d.drawTextRegion("story description", description, options.FontPath, height*6/100, layout.description, width, height, false); err != nil {
			return err
		}
	}
	if source := storySourceLabel(story.Sources); source != "" {
		if err := d.drawTextRegion("story source", source, options.FontPath, height*3/100, layout.source, width, height, false); err != nil {
			return err
		}
	}
	return nil
}

func (d *Device) drawStoryOverlay(story Story, options StoryOptions, layout storyLayout, width, height int) error {
	genre := strings.ToUpper(strings.TrimSpace(story.Genre))
	if genre == "" {
		genre = "NEWS"
	}
	if err := d.drawOverlayTextRegion("story genre", genre, options.FontPath, height*4/100, layout.genre, width, height); err != nil {
		return err
	}
	if err := d.drawOverlayTextRegion("story title", story.Title, clockTimeFont, height*15/100, layout.title, width, height); err != nil {
		return err
	}
	if description := strings.TrimSpace(story.Description); description != "" {
		if err := d.drawOverlayTextRegion("story description", description, options.FontPath, height*6/100, layout.description, width, height); err != nil {
			return err
		}
	}
	if source := storySourceLabel(story.Sources); source != "" {
		if err := d.drawOverlayTextRegion("story source", source, options.FontPath, height*3/100, layout.source, width, height); err != nil {
			return err
		}
	}
	return nil
}

func (d *Device) drawOverlayTextRegion(name, value, font string, fontSize int, box displayRect, screenWidth, screenHeight int) error {
	if box.width < 2 || box.height < 2 {
		return fmt.Errorf("story: draw %s: region too small (%dx%d)", name, box.width, box.height)
	}
	if fontSize > box.height*96/100 {
		fontSize = max(1, box.height*96/100)
	}
	// -O (bgless) keeps the image under the glyphs. FBInk still needs a pen
	// background that differs from the foreground: with the default white
	// background, white bgless text blends to a no-op and nothing is drawn.
	// -B BLACK is only used for that pen math; it is not painted.
	command := fmt.Sprintf(`%s -q -w -W GC16 -O -C WHITE -B BLACK -t %s -- %s`,
		fbinkPath, shellQuote(typeSpecNoPad(font, fontSize, box, screenWidth, screenHeight)), shellQuote(value))
	if _, err := d.client.Run(command); err != nil {
		return fmt.Errorf("story: draw %s: %w", name, err)
	}
	return nil
}

func storySourceLabel(sources []StorySource) string {
	seen := make(map[string]bool)
	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		label := strings.TrimSpace(source.Domain)
		if label != "" && !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	if len(labels) == 0 {
		return ""
	}
	return "SOURCE  ·  " + strings.Join(labels, "  /  ")
}
