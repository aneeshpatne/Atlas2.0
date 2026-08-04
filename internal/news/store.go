// Package news loads news collections from Redis.
package news

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aneeshpatne/atlas/internal/redis"
)

const (
	genresKey       = "genre"
	collectionKeyFmt = "news_collection_v2:%s"
)

// Store reads news data from Redis.
type Store struct {
	redis *redis.Client
}

// NewStore returns a news store backed by the given Redis client.
func NewStore(client *redis.Client) *Store {
	return &Store{redis: client}
}

// Genres returns the set of known news genres.
func (s *Store) Genres(ctx context.Context) ([]string, error) {
	res, err := s.redis.Raw().SMembers(ctx, genresKey).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers %s: %w", genresKey, err)
	}
	return res, nil
}

// ByGenre returns raw news JSON strings for a genre (list order preserved).
func (s *Store) ByGenre(ctx context.Context, genre string) ([]string, error) {
	key := fmt.Sprintf(collectionKeyFmt, genre)
	res, err := s.redis.Raw().LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("lrange %s: %w", key, err)
	}
	return res, nil
}

// StoriesByGenre decodes each news item for a genre into Story values.
// Invalid items are skipped; if decoding fails for every item, an error is returned
// only when the list is non-empty and none decoded.
func (s *Store) StoriesByGenre(ctx context.Context, genre string) ([]Story, error) {
	raw, err := s.ByGenre(ctx, genre)
	if err != nil {
		return nil, err
	}
	stories := make([]Story, 0, len(raw))
	var firstErr error
	for i, item := range raw {
		var story Story
		if err := json.Unmarshal([]byte(item), &story); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("decode news item %d for genre %q: %w", i, genre, err)
			}
			continue
		}
		stories = append(stories, story)
	}
	if len(raw) > 0 && len(stories) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return stories, nil
}
