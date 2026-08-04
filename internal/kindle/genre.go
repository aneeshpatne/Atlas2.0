package kindle

import (
	"fmt"
	"log"
	"strings"
)

const remoteGenreImage = "/tmp/atlas-genre-image"

// ShowGenreScreen paints an optional full-screen backdrop, then the genre label.
// backdrop may be empty for a plain clear + text screen.
func (d *Device) ShowGenreScreen(genre string, backdrop []byte) error {
	genre = strings.TrimSpace(genre)
	if genre == "" {
		return fmt.Errorf("genre screen: genre is required")
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

	if len(backdrop) > 0 {
		uploader, ok := d.client.(interface{ Upload(string, []byte) error })
		if !ok {
			log.Printf("genre: backdrop skipped: SSH client cannot upload files")
		} else {
			data, err := prepareStoryBackground(backdrop, width, height)
			if err != nil {
				log.Printf("genre: backdrop skipped: prepare: %v", err)
			} else if err := uploader.Upload(remoteGenreImage, data); err != nil {
				log.Printf("genre: backdrop skipped: upload: %v", err)
			} else {
				spec := fmt.Sprintf("x=0,y=0,w=%d,h=%d,dither", width, height)
				command := fmt.Sprintf("%s -q -w -W GC16 -i %s -g %s", fbinkPath, shellQuote(remoteGenreImage), shellQuote(spec))
				if _, err := d.client.Run(command); err != nil {
					log.Printf("genre: backdrop skipped: draw: %v", err)
				}
				_, _ = d.client.Run("rm -f " + remoteGenreImage)
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
		if _, err := d.client.Run(command); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("show genre screen: %w", lastErr)
}
