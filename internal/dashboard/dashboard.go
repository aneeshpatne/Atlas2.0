// Package dashboard orchestrates news loading and Kindle display.
package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aneeshpatne/atlas/internal/kindle"
	"github.com/aneeshpatne/atlas/internal/news"
)

const (
	defaultGenreHold = 10 * time.Second
	defaultStoryHold = 10 * time.Second
)

// Dashboard loads news from Redis and shows it on a Kindle.
type Dashboard struct {
	store  *news.Store
	device *kindle.Device
}

// New returns a dashboard wired to a news store and Kindle device.
func New(store *news.Store, device *kindle.Device) *Dashboard {
	return &Dashboard{
		store:  store,
		device: device,
	}
}

// Options controls how ShowNews loads and paints stories.
type Options struct {
	// Genre limits the loop to one genre. Empty means every genre from Redis.
	Genre string
	// FontPath is the TTF path on the Kindle used for story type.
	FontPath string
	// AssetsDir is the local directory of genre backdrop images
	// (e.g. assets/genres). Matching is case-insensitive; misc.* is the fallback.
	AssetsDir string
	// GenreHold is how long the genre title screen stays up. Default 10s.
	GenreHold time.Duration
	// StoryHold is how long each story stays up. Default 10s.
	StoryHold time.Duration
	// MaxStoriesPerGenre limits each pass through a genre. Zero means all stories.
	MaxStoriesPerGenre int
}

// ShowNews walks genres, shows each genre screen, then rotates+displays stories
// for StoryHold each, until the parent context is cancelled.
//
// Flow per genre:
//  1. clear + show genre name
//  2. hold GenreHold
//  3. LMOVE once per queued story (full pass, no deletes), RunStory each for StoryHold
//
// After all genres, starts over.
func (d *Dashboard) ShowNews(ctx context.Context, options Options) error {
	if d.store == nil {
		return fmt.Errorf("dashboard: news store is required")
	}
	if d.device == nil {
		return fmt.Errorf("dashboard: kindle device is required")
	}
	if options.GenreHold <= 0 {
		options.GenreHold = defaultGenreHold
	}
	if options.StoryHold <= 0 {
		options.StoryHold = defaultStoryHold
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := d.ShowNewsCycle(ctx, options); err != nil {
			return err
		}
	}
}

// ShowNewsCycle renders one genre-wise pass and returns. It is useful to a
// supervisor that needs to interrupt a pass on demand and then start a fresh
// pass after a configured refresh interval.
func (d *Dashboard) ShowNewsCycle(ctx context.Context, options Options) error {
	if d.store == nil {
		return fmt.Errorf("dashboard: news store is required")
	}
	if d.device == nil {
		return fmt.Errorf("dashboard: kindle device is required")
	}
	if options.GenreHold <= 0 {
		options.GenreHold = defaultGenreHold
	}
	if options.StoryHold <= 0 {
		options.StoryHold = defaultStoryHold
	}
	genres, err := d.genres(ctx, options.Genre)
	if err != nil {
		return err
	}
	if len(genres) == 0 {
		log.Printf("dashboard: no genres in redis")
		return wait(ctx, options.GenreHold)
	}
	for _, genre := range genres {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := d.showGenre(genre, options.AssetsDir); err != nil {
			return fmt.Errorf("show genre %q: %w", genre, err)
		}
		if err := wait(ctx, options.GenreHold); err != nil {
			return nil
		}
		if err := d.drainGenre(ctx, genre, options); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dashboard) genres(ctx context.Context, filter string) ([]string, error) {
	if strings.TrimSpace(filter) != "" {
		return []string{filter}, nil
	}
	genres, err := d.store.Genres(ctx)
	if err != nil {
		return nil, fmt.Errorf("get genres: %w", err)
	}
	out := make([]string, 0, len(genres))
	for _, g := range genres {
		if strings.TrimSpace(g) != "" {
			out = append(out, g)
		}
	}
	return out, nil
}

func (d *Dashboard) showGenre(genre, assetsDir string) error {
	backdrop, path, err := loadGenreBackdrop(assetsDir, genre)
	if err != nil {
		return fmt.Errorf("genre backdrop for %q: %w", genre, err)
	}
	log.Printf("dashboard: genre %q backdrop %s", genre, path)
	return d.device.ShowGenreScreen(genre, backdrop)
}

// loadGenreBackdrop loads assets/genres/<genre>.* (case-insensitive), falling
// back to misc.*. Every genre screen is expected to have a photo background.
func loadGenreBackdrop(assetsDir, genre string) ([]byte, string, error) {
	dir := strings.TrimSpace(assetsDir)
	if dir == "" {
		dir = "assets/genres"
	}
	return loadBackdrop(dir, genre)
}

// drainGenre shows each story currently in the genre queue once via LMOVE rotate.
func (d *Dashboard) drainGenre(ctx context.Context, genre string, options Options) error {
	n, err := d.store.Len(ctx, genre)
	if err != nil {
		return fmt.Errorf("len genre %q: %w", genre, err)
	}
	if options.MaxStoriesPerGenre > 0 && n > int64(options.MaxStoriesPerGenre) {
		n = int64(options.MaxStoriesPerGenre)
	}
	// Genre asset is the story fallback when a source has no usable ogurl.
	genreBackdrop, backdropPath, err := loadGenreBackdrop(options.AssetsDir, genre)
	if err != nil {
		log.Printf("dashboard: genre %q story fallback backdrop unavailable: %v", genre, err)
		genreBackdrop = nil
	} else {
		log.Printf("dashboard: genre %q story fallback backdrop %s", genre, backdropPath)
	}
	for i := int64(0); i < n; i++ {
		if err := ctx.Err(); err != nil {
			return nil
		}
		story, ok, err := d.store.Next(ctx, genre)
		if err != nil {
			return fmt.Errorf("next genre %q: %w", genre, err)
		}
		if !ok {
			return nil
		}
		if strings.TrimSpace(story.Title) == "" {
			log.Printf("dashboard: skip empty-title story in genre %q", genre)
			continue
		}
		if strings.TrimSpace(story.Genre) == "" {
			story.Genre = genre
		}
		storyCtx, cancel := context.WithTimeout(ctx, options.StoryHold)
		err = d.device.RunStory(storyCtx, toKindleStory(story), kindle.StoryOptions{
			FontPath:      options.FontPath,
			FallbackImage: genreBackdrop,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("run story %q: %w", story.Title, err)
		}
		if err := ctx.Err(); err != nil {
			return nil
		}
	}
	return nil
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func toKindleStory(story news.Story) kindle.Story {
	sources := make([]kindle.StorySource, len(story.Sources))
	for i, source := range story.Sources {
		sources[i] = kindle.StorySource{
			URL:    source.URL,
			Domain: source.Domain,
			OGURL:  source.OGURL,
		}
	}
	return kindle.Story{
		StoryID:     story.StoryID,
		EventID:     story.EventID,
		Title:       story.Title,
		Description: story.Description,
		Genre:       story.Genre,
		Sources:     sources,
	}
}
