// Package news loads and queues news collections in Redis.
package news

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"github.com/aneeshpatne/atlas/internal/redis"
)

const (
	genresKey        = "genre"
	collectionKeyFmt = "news_collection_v2:%s"
)

// Store reads and mutates news queues in Redis.
type Store struct {
	redis *redis.Client
}

// NewStore returns a news store backed by the given Redis client.
func NewStore(client *redis.Client) *Store {
	return &Store{redis: client}
}

func collectionKey(genre string) string {
	return fmt.Sprintf(collectionKeyFmt, genre)
}

// Genres returns the set of known news genres.
func (s *Store) Genres(ctx context.Context) ([]string, error) {
	res, err := s.redis.Raw().SMembers(ctx, genresKey).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers %s: %w", genresKey, err)
	}
	return res, nil
}

// Push appends a story to the end of a genre queue (RPUSH) and records the genre.
func (s *Store) Push(ctx context.Context, genre string, story Story) error {
	if genre == "" {
		return fmt.Errorf("push: genre is required")
	}
	data, err := json.Marshal(story)
	if err != nil {
		return fmt.Errorf("push: marshal story: %w", err)
	}
	key := collectionKey(genre)
	pipe := s.redis.Raw().Pipeline()
	pipe.RPush(ctx, key, data)
	pipe.SAdd(ctx, genresKey, genre)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("rpush %s: %w", key, err)
	}
	return nil
}

// Next rotates a genre queue with LMOVE (RIGHT → LEFT, same key) and returns that story.
// Equivalent to RPOPLPUSH: item moves from the tail to the head, so nothing is deleted.
// ok is false when the queue is empty.
func (s *Store) Next(ctx context.Context, genre string) (story Story, ok bool, err error) {
	key := collectionKey(genre)
	res, err := s.redis.Raw().LMove(ctx, key, key, "RIGHT", "LEFT").Result()
	if err == goredis.Nil {
		return Story{}, false, nil
	}
	if err != nil {
		return Story{}, false, fmt.Errorf("lmove %s: %w", key, err)
	}
	if err := json.Unmarshal([]byte(res), &story); err != nil {
		return Story{}, false, fmt.Errorf("decode news item for genre %q: %w", genre, err)
	}
	return story, true, nil
}

// Len returns how many stories are queued for a genre.
func (s *Store) Len(ctx context.Context, genre string) (int64, error) {
	key := collectionKey(genre)
	n, err := s.redis.Raw().LLen(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("llen %s: %w", key, err)
	}
	return n, nil
}

// ByGenre returns raw news JSON strings for a genre without consuming them.
func (s *Store) ByGenre(ctx context.Context, genre string) ([]string, error) {
	key := collectionKey(genre)
	res, err := s.redis.Raw().LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("lrange %s: %w", key, err)
	}
	return res, nil
}

// StoriesByGenre decodes each news item for a genre into Story values (non-destructive).
// Invalid items are skipped; if the list is non-empty and none decode, the first error is returned.
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
