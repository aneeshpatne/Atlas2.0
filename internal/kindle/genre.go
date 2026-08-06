package kindle

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

const remoteGenreImage = "/tmp/atlas-genre-image"

// ShowGenreScreen paints an optional full-screen backdrop, then the genre label.
// backdrop may be empty for a plain clear + text screen.
func (d *Device) ShowGenreScreen(genre string, backdrop []byte) error {
	return d.ShowGenreScreenContext(context.Background(), genre, backdrop)
}

func (d *Device) ShowGenreScreenContext(ctx context.Context, genre string, backdrop []byte) error {
	genre = strings.TrimSpace(genre)
	if genre == "" {
		return fmt.Errorf("genre screen: genre is required")
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

	if len(backdrop) > 0 {
		_, hasContextUploader := d.client.(contextUploader)
		_, hasLegacyUploader := d.client.(interface{ Upload(string, []byte) error })
		if !hasContextUploader && !hasLegacyUploader {
			slog.Warn("genre backdrop skipped", "error", "SSH client cannot upload files")
		} else {
			data, err := prepareStoryBackground(backdrop, width, height)
			if err != nil {
				slog.Warn("genre backdrop preparation failed", "error", err)
			} else if err := d.upload(ctx, remoteGenreImage, data); err != nil {
				slog.Warn("genre backdrop upload failed", "error", err)
			} else {
				spec := fmt.Sprintf("x=0,y=0,w=%d,h=%d,dither", width, height)
				command := fmt.Sprintf("%s -q -w -W GC16 -i %s -g %s", fbinkPath, shellQuote(remoteGenreImage), shellQuote(spec))
				if _, err := d.run(ctx, command); err != nil {
					slog.Warn("genre backdrop draw failed", "error", err)
				}
				_, _ = d.run(ctx, "rm -f "+shellQuote(remoteGenreImage))
			}
		}
	}

	// White bgless type over the photo (same pen trick as story overlay).
	fontSize := height * 12 / 100
	if fontSize < 48 {
		fontSize = 48
	}
	top := height * 35 / 100
	boxHeight := height * 30 / 100
	// Prefer InstrumentSerif when present; fall back to the clock default font.
	typeSpecs := []string{
		fmt.Sprintf("regular=/mnt/us/fonts/InstrumentSerif-Regular.ttf,px=%d,top=%d,left=0,right=0,bottom=%d",
			fontSize, top, height-top-boxHeight),
		fmt.Sprintf("regular=%s,px=%d,top=%d,left=0,right=0,bottom=%d",
			defaultClockFont, fontSize, top, height-top-boxHeight),
	}
	var lastErr error
	for _, typeSpec := range typeSpecs {
		command := fmt.Sprintf(`%s -q -w -W GC16 -O -C WHITE -B BLACK -m -t %s -- %s`,
			fbinkPath, shellQuote(typeSpec), shellQuote(strings.ToUpper(genre)))
		if _, err := d.run(ctx, command); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("show genre screen: %w", lastErr)
}
