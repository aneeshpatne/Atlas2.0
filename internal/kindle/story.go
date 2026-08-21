package kindle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const remoteStoryImage = "/tmp/atlas-story-image"

const (
	// Bump this whenever the display conversion changes so an older prepared
	// image can never be mistaken for output from the current pipeline.
	storyImageCacheVersion = "eink-v3"
	maxPreparedImages      = 128
	maxPreparedImageBytes  = 64 << 20
)

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
	FontPath          string
	FetchImage        func(context.Context, string) ([]byte, error)
	AllowPrivateImage bool
	// FallbackImage is raw image bytes (e.g. genre asset from assets/genres)
	// used when no OG image is available or fetch/prepare fails.
	FallbackImage []byte
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
		genre:       displayRect{top: height * 6 / 100, left: contentLeft, width: contentWidth, height: height * 8 / 100},
		genreRule:   displayRect{top: height * 15 / 100, left: contentLeft, width: min(width*13/100, contentWidth), height: line},
		title:       displayRect{top: height * 18 / 100, left: contentLeft, width: contentWidth, height: height * 42 / 100},
		description: displayRect{top: height * 62 / 100, left: contentLeft, width: contentWidth, height: height * 24 / 100},
		source:      displayRect{top: height * 88 / 100, left: contentLeft, width: contentWidth, height: height * 6 / 100},
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
		options.FetchImage = func(ctx context.Context, raw string) ([]byte, error) {
			return fetchStoryImageWithPolicy(ctx, raw, options.AllowPrivateImage)
		}
	}
	if err := d.SetRotationContext(ctx, "horizontal"); err != nil {
		return err
	}
	if err := d.ClearScreenContext(ctx); err != nil {
		return err
	}
	if err := d.SetBacklightContext(ctx, 20); err != nil {
		return err
	}
	width, height, err := d.displaySizeContext(ctx)
	if err != nil {
		return err
	}
	imageReady := false
	_, hasContextUploader := d.client.(contextUploader)
	_, hasLegacyUploader := d.client.(interface{ Upload(string, []byte) error })
	if !hasContextUploader && !hasLegacyUploader {
		slog.Warn("story background skipped", "error", "SSH client cannot upload files")
	} else {
		// Prefer Open Graph image from sources (ogurl).
		if imageURL := firstStoryImageURL(story.Sources); imageURL != "" {
			cacheKey := preparedImageCacheKey("url", imageURL, width, height)
			data, ok := d.getPreparedImage(cacheKey)
			if ok {
				err = nil
			} else {
				data, err = options.FetchImage(ctx, imageURL)
				if err == nil {
					data, err = prepareStoryBackground(data, width, height)
				}
				if err == nil {
					d.putPreparedImage(cacheKey, data)
				}
			}
			if err != nil {
				slog.Warn("story image fetch or preparation failed", "url", redactURL(imageURL), "error", err)
			} else if err := d.upload(ctx, remoteStoryImage, data); err != nil {
				slog.Warn("story image upload failed", "error", err)
			} else {
				imageReady = true
			}
		} else {
			slog.Debug("story has no image URL; trying fallback", "title", story.Title)
		}
		// Fall back to genre asset (assets/genres) so every story still has a photo bg.
		if !imageReady && len(options.FallbackImage) > 0 {
			hash := sha256.Sum256(options.FallbackImage)
			cacheKey := preparedImageCacheKey("fallback", fmt.Sprintf("%x", hash), width, height)
			data, ok := d.getPreparedImage(cacheKey)
			if ok {
				err = nil
			} else {
				data, err = prepareStoryBackground(options.FallbackImage, width, height)
				if err == nil {
					d.putPreparedImage(cacheKey, data)
				}
			}
			if err != nil {
				slog.Warn("story fallback preparation failed", "error", err)
			} else if err := d.upload(ctx, remoteStoryImage, data); err != nil {
				slog.Warn("story fallback upload failed", "error", err)
			} else {
				slog.Debug("using genre fallback background", "title", story.Title)
				imageReady = true
			}
		}
		if !imageReady {
			slog.Warn("story has no usable background", "title", story.Title)
		}
	}
	layout := newStoryLayout(width, height, imageReady)
	if imageReady {
		if err := d.drawStoryImageContext(ctx, layout.image); err != nil {
			slog.Warn("story background draw failed", "error", err)
			layout = newStoryLayout(width, height, false)
		}
		_, _ = d.run(ctx, "rm -f "+shellQuote(remoteStoryImage))
	}
	if err := d.drawStoryContext(ctx, story, options, layout, width, height); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (d *Device) getPreparedImage(key string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, ok := d.preparedImages[key]
	if !ok {
		return nil, false
	}
	// Promote on access so frequently repeated stories survive cache pressure.
	for i, candidate := range d.preparedImageOrder {
		if candidate == key {
			copy(d.preparedImageOrder[i:], d.preparedImageOrder[i+1:])
			d.preparedImageOrder[len(d.preparedImageOrder)-1] = key
			break
		}
	}
	return append([]byte(nil), data...), true
}

func (d *Device) putPreparedImage(key string, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.preparedImages == nil {
		d.preparedImages = make(map[string][]byte)
	}
	if _, exists := d.preparedImages[key]; exists {
		return
	}
	if len(data) > maxPreparedImageBytes {
		return
	}
	for len(d.preparedImageOrder) > 0 &&
		(len(d.preparedImageOrder) >= maxPreparedImages || d.preparedImageBytes+len(data) > maxPreparedImageBytes) {
		oldest := d.preparedImageOrder[0]
		d.preparedImageOrder = d.preparedImageOrder[1:]
		d.preparedImageBytes -= len(d.preparedImages[oldest])
		delete(d.preparedImages, oldest)
	}
	d.preparedImages[key] = append([]byte(nil), data...)
	d.preparedImageBytes += len(data)
	d.preparedImageOrder = append(d.preparedImageOrder, key)
}

func preparedImageCacheKey(kind, identity string, width, height int) string {
	hash := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%s:%s:%x:%dx%d", storyImageCacheVersion, kind, hash, width, height)
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (d *Device) drawStoryImage(box displayRect) error {
	return d.drawStoryImageContext(context.Background(), box)
}

func (d *Device) drawStoryImageContext(ctx context.Context, box displayRect) error {
	spec := fmt.Sprintf("x=%d,y=%d,w=%d,h=%d,dither", box.left, box.top, box.width, box.height)
	// Complete the background refresh before any overlay text is drawn.
	command := fmt.Sprintf("%s -q -w -W GC16 -i %s -g %s", fbinkPath, shellQuote(remoteStoryImage), shellQuote(spec))
	if _, err := d.run(ctx, command); err != nil {
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

func fetchStoryImage(ctx context.Context, imageURL string) ([]byte, error) {
	return fetchStoryImageWithPolicy(ctx, imageURL, false)
}

func fetchStoryImageWithPolicy(ctx context.Context, imageURL string, allowPrivate bool) ([]byte, error) {
	data, err := getStoryImageURL(ctx, imageURL, allowPrivate)
	if err == nil {
		return data, nil
	}
	// CDNs often put resize/crop params in the query string and gate those
	// transforms harder than the original asset. Retry the bare path once.
	if stripped := stripURLQuery(imageURL); stripped != imageURL {
		if retry, retryErr := getStoryImageURL(ctx, stripped, allowPrivate); retryErr == nil {
			return retry, nil
		}
	}
	return nil, err
}

func getStoryImageURL(ctx context.Context, imageURL string, allowPrivate bool) ([]byte, error) {
	if err := validateImageURL(ctx, imageURL, allowPrivate); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	// Browser-like headers: many news CDNs (Akamai, etc.) 403 bare Go clients.
	request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	request.Header.Set("Accept", "image/webp,image/png,image/jpeg,image/gif,image/*;q=0.8")
	if referer := refererForImageURL(imageURL); referer != "" {
		request.Header.Set("Referer", referer)
	}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: imageDialer(allowPrivate)},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many image redirects")
			}
			return validateImageURL(req.Context(), req.URL.String(), allowPrivate)
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("image request returned %s", response.Status)
	}
	const maxImageBytes = 12 << 20
	if response.ContentLength > maxImageBytes {
		return nil, fmt.Errorf("image exceeds 12 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return nil, fmt.Errorf("image size must be between 1 byte and 12 MiB")
	}
	// Reject HTML error pages that some CDNs return with a 200.
	if ctype := strings.ToLower(response.Header.Get("Content-Type")); ctype != "" &&
		!strings.HasPrefix(ctype, "image/") && !strings.HasPrefix(ctype, "application/octet-stream") {
		return nil, fmt.Errorf("image request returned non-image content-type %q", ctype)
	}
	if looksLikeHTML(data) {
		return nil, fmt.Errorf("image request returned HTML instead of image data")
	}
	return data, nil
}

func validatePublicImageURL(ctx context.Context, raw string) error {
	return validateImageURL(ctx, raw, false)
}

func validateImageURL(ctx context.Context, raw string, allowPrivate bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("image URL must be public HTTPS without credentials")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil {
		return fmt.Errorf("resolve image host: %w", err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("image host has no addresses")
	}
	for _, address := range addresses {
		if !allowPrivate && !isPublicIP(address.IP) {
			return fmt.Errorf("image URL resolves to a private or local address")
		}
	}
	return nil
}

func imageDialer(allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, candidate := range addresses {
			if !allowPrivate && !isPublicIP(candidate.IP) {
				continue
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		}
		return nil, fmt.Errorf("image host has no permitted address")
	}
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func stripURLQuery(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawQuery == "" {
		return raw
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func refererForImageURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	// Prefer the publisher origin over the CDN host when the host looks like a CDN.
	host := strings.ToLower(parsed.Host)
	if strings.Contains(host, "ndtvimg") {
		return "https://www.ndtv.com/"
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}

func looksLikeHTML(data []byte) bool {
	trim := bytes.TrimSpace(data)
	if len(trim) < 15 {
		return false
	}
	prefix := bytes.ToLower(trim[:min(64, len(trim))])
	return bytes.HasPrefix(prefix, []byte("<!doctype html")) ||
		bytes.HasPrefix(prefix, []byte("<html")) ||
		bytes.Contains(prefix, []byte("<html"))
}

// storyScrimPeakPercent is the extra mid-frame darkening over the copy stack.
// 0 disables the scrim.
const storyScrimPeakPercent = 25

// E-ink needs a narrower useful range than a backlit display. Keeping a floor
// retains detail in dark clothes/hair, while the ceiling leaves white overlay
// text clearly separated from even the brightest part of the photograph.
const (
	// Keep a small non-zero floor so near-black source detail survives the
	// Kindle's coarse 16-level grayscale conversion.
	storyShadowFloor = 16
	// White type needs a wide luminance gap from the photo. This ceiling leaves
	// roughly eight useful e-ink steps below white, even before the copy scrim.
	storyHighlightCeiling = 132
	// Slightly darken midtones while retaining a smooth, detail-preserving toe.
	storyToneGamma = 0.92
	// Restore small-scale separation after resizing so the darker output retains
	// faces, clothing folds, and background edges on a 16-level panel.
	storyLocalContrastPercent = 40
)

// storyScrimPercent returns extra darkening (0–100) for a vertical position so
// the genre/title/description bands stay a bit darker than the photo edges.
func storyScrimPercent(yPct int) int {
	switch {
	case yPct < 8:
		return yPct * storyScrimPeakPercent / 8 // ramp in from the top edge
	case yPct < 82:
		return storyScrimPeakPercent // steady veil over the main copy stack
	case yPct < 100:
		// Ease off toward the bottom so the photo isn't a flat black slab.
		return storyScrimPeakPercent * (100 - yPct) / 18
	default:
		return 0
	}
}

// prepareStoryBackground turns a source image into a display-sized grayscale
// PNG tuned for a 16-level e-ink panel. It expands useful source contrast, then
// maps it into a bounded range instead of multiplying every pixel darker (which
// crushes already-dark details to black).
func prepareStoryBackground(data []byte, width, height int) ([]byte, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image configuration: %w", err)
	}
	if config.Width < 1 || config.Height < 1 || config.Width > 10000 || config.Height > 10000 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return nil, fmt.Errorf("image dimensions are not supported")
	}
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

	resized := image.NewGray(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(resized, resized.Bounds(), source, srcBounds, draw.Over, nil)
	detail := enhanceLocalContrast(resized, max(2, min(width, height)/80), storyLocalContrastPercent)

	// Ignore the extreme two percent at each end. A small black logo or white
	// patch should not prevent the photograph itself from using the e-ink range.
	low, high := grayscalePercentiles(detail, 2, 98)
	if high-low < 32 {
		// Nearly flat images look more natural without aggressive stretching.
		low, high = 0, 255
	}

	background := image.NewGray(resized.Bounds())
	for y := 0; y < height; y++ {
		// Copy-band scrim: push the title and description backdrop down another
		// few e-ink levels so antialiased white glyphs remain distinct.
		yPct := y * 100 / height
		scrim := storyScrimPercent(yPct)
		for x := 0; x < width; x++ {
			luma := einkTone(int(detail.GrayAt(x, y).Y), low, high)
			luma = luma * (100 - scrim) / 100
			background.SetGray(x, y, color.Gray{Y: uint8(luma)})
		}
	}

	var output bytes.Buffer
	if err := png.Encode(&output, background); err != nil {
		return nil, fmt.Errorf("encode background: %w", err)
	}
	return output.Bytes(), nil
}

// enhanceLocalContrast applies a restrained luminance-only unsharp mask. It
// improves local separation without introducing color artifacts or requiring
// the e-ink panel to reproduce a wider brightness range.
func enhanceLocalContrast(source *image.Gray, radius, amountPercent int) *image.Gray {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width == 0 || height == 0 || radius < 1 || amountPercent <= 0 {
		clone := image.NewGray(image.Rect(0, 0, width, height))
		draw.Draw(clone, clone.Bounds(), source, bounds.Min, draw.Src)
		return clone
	}

	// Integral luminance makes each local-mean lookup constant time.
	stride := width + 1
	integral := make([]int64, stride*(height+1))
	for y := 0; y < height; y++ {
		rowSum := int64(0)
		for x := 0; x < width; x++ {
			rowSum += int64(source.GrayAt(bounds.Min.X+x, bounds.Min.Y+y).Y)
			integral[(y+1)*stride+x+1] = integral[y*stride+x+1] + rowSum
		}
	}

	result := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		y0, y1 := max(0, y-radius), min(height, y+radius+1)
		for x := 0; x < width; x++ {
			x0, x1 := max(0, x-radius), min(width, x+radius+1)
			sum := integral[y1*stride+x1] - integral[y0*stride+x1] - integral[y1*stride+x0] + integral[y0*stride+x0]
			mean := int(sum / int64((x1-x0)*(y1-y0)))
			center := int(source.GrayAt(bounds.Min.X+x, bounds.Min.Y+y).Y)
			enhanced := center + (center-mean)*amountPercent/100
			result.SetGray(x, y, color.Gray{Y: uint8(max(0, min(255, enhanced)))})
		}
	}
	return result
}

func grayscalePercentiles(source *image.Gray, lowPercent, highPercent int) (int, int) {
	var histogram [256]int
	for y := source.Bounds().Min.Y; y < source.Bounds().Max.Y; y++ {
		for x := source.Bounds().Min.X; x < source.Bounds().Max.X; x++ {
			histogram[source.GrayAt(x, y).Y]++
		}
	}
	total := source.Bounds().Dx() * source.Bounds().Dy()
	valueAt := func(percent int) int {
		target := max(1, total*percent/100)
		seen := 0
		for value, count := range histogram {
			seen += count
			if seen >= target {
				return value
			}
		}
		return 255
	}
	return valueAt(lowPercent), valueAt(highPercent)
}

func einkTone(luma, low, high int) int {
	if luma <= low {
		return storyShadowFloor
	}
	if luma >= high {
		return storyHighlightCeiling
	}
	normalized := float64(luma-low) / float64(high-low)
	curved := math.Pow(normalized, storyToneGamma)
	return storyShadowFloor + int(math.Round(curved*float64(storyHighlightCeiling-storyShadowFloor)))
}

func (d *Device) drawStory(story Story, options StoryOptions, layout storyLayout, width, height int) error {
	return d.drawStoryContext(context.Background(), story, options, layout, width, height)
}

func (d *Device) drawStoryContext(ctx context.Context, story Story, options StoryOptions, layout storyLayout, width, height int) error {
	if layout.image.width >= 2 && layout.image.height >= 2 {
		return d.drawStoryOverlayContext(ctx, story, options, layout, width, height)
	}
	genre := strings.ToUpper(strings.TrimSpace(story.Genre))
	if genre == "" {
		genre = "NEWS"
	}
	if err := d.drawTextRegionContext(ctx, "story genre", genre, options.FontPath, height*5/100, layout.genre, width, height, false); err != nil {
		return err
	}
	if err := d.fillRectContext(ctx, layout.genreRule, true); err != nil {
		return fmt.Errorf("story: genre rule: %w", err)
	}
	if err := d.drawTextRegionContext(ctx, "story title", story.Title, clockTimeFont, height*18/100, layout.title, width, height, false); err != nil {
		return err
	}
	if description := strings.TrimSpace(story.Description); description != "" {
		if err := d.drawTextRegionContext(ctx, "story description", description, options.FontPath, height*7/100, layout.description, width, height, false); err != nil {
			return err
		}
	}
	if source := storySourceLabel(story.Sources); source != "" {
		if err := d.drawTextRegionContext(ctx, "story source", source, options.FontPath, height*4/100, layout.source, width, height, false); err != nil {
			return err
		}
	}
	return nil
}

func (d *Device) drawStoryOverlay(story Story, options StoryOptions, layout storyLayout, width, height int) error {
	return d.drawStoryOverlayContext(context.Background(), story, options, layout, width, height)
}

func (d *Device) drawStoryOverlayContext(ctx context.Context, story Story, options StoryOptions, layout storyLayout, width, height int) error {
	genre := strings.ToUpper(strings.TrimSpace(story.Genre))
	if genre == "" {
		genre = "NEWS"
	}
	commands := make([]string, 0, 5)
	add := func(name, value, font string, size int, box displayRect) error {
		command, err := overlayTextCommand(name, value, font, size, box, width, height, true)
		if err == nil {
			commands = append(commands, command)
		}
		return err
	}
	if err := add("story genre", genre, options.FontPath, height*5/100, layout.genre); err != nil {
		return err
	}
	if err := add("story title", story.Title, clockTimeFont, height*18/100, layout.title); err != nil {
		return err
	}
	if description := strings.TrimSpace(story.Description); description != "" {
		if err := add("story description", description, options.FontPath, height*7/100, layout.description); err != nil {
			return err
		}
	}
	if source := storySourceLabel(story.Sources); source != "" {
		if err := add("story source", source, options.FontPath, height*4/100, layout.source); err != nil {
			return err
		}
	}
	commands = append(commands, fmt.Sprintf(`%s -q -W GC16 -s %s`, fbinkPath, regionSpec(displayRect{width: width, height: height})))
	if _, err := d.run(ctx, strings.Join(commands, " && ")); err != nil {
		return fmt.Errorf("story: draw overlay: %w", err)
	}
	return nil
}

func (d *Device) drawOverlayTextRegion(name, value, font string, fontSize int, box displayRect, screenWidth, screenHeight int) error {
	return d.drawOverlayTextRegionContext(context.Background(), name, value, font, fontSize, box, screenWidth, screenHeight)
}

func (d *Device) drawOverlayTextRegionContext(ctx context.Context, name, value, font string, fontSize int, box displayRect, screenWidth, screenHeight int) error {
	command, err := overlayTextCommand(name, value, font, fontSize, box, screenWidth, screenHeight, false)
	if err != nil {
		return err
	}
	if _, err := d.run(ctx, command); err != nil {
		return fmt.Errorf("story: draw %s: %w", name, err)
	}
	return nil
}

func overlayTextCommand(name, value, font string, fontSize int, box displayRect, screenWidth, screenHeight int, buffered bool) (string, error) {
	if box.width < 2 || box.height < 2 {
		return "", fmt.Errorf("story: draw %s: region too small (%dx%d)", name, box.width, box.height)
	}
	if fontSize > box.height*96/100 {
		fontSize = max(1, box.height*96/100)
	}
	// -O (bgless) keeps the image under the glyphs. FBInk still needs a pen
	// background that differs from the foreground: with the default white
	// background, white bgless text blends to a no-op and nothing is drawn.
	// -B BLACK is only used for that pen math; it is not painted.
	mode := "-w -W GC16"
	if buffered {
		mode = "-b"
	}
	return fmt.Sprintf(`%s -q %s -O -C WHITE -B BLACK -t %s -- %s`,
		fbinkPath, mode, shellQuote(typeSpecNoPad(font, fontSize, box, screenWidth, screenHeight)), shellQuote(value)), nil
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
