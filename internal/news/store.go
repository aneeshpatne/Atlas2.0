// Package news loads and queues news collections in Redis.
package news

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/aneeshpatne/atlas/internal/redis"
)

const (
	genresKey        = "genre"
	collectionKeyFmt = "news_collection_v2:%s"
)

type StoreOptions struct {
	KeyPrefix        string
	MaxStories       int64
	DeduplicationTTL time.Duration
}

// Store reads and mutates news queues in Redis.
type Store struct {
	redis   *redis.Client
	options StoreOptions
}

// NewStore returns a news store backed by the given Redis client.
func NewStore(client *redis.Client) *Store {
	return NewStoreWithOptions(client, StoreOptions{})
}

func NewStoreWithOptions(client *redis.Client, options StoreOptions) *Store {
	if options.MaxStories <= 0 {
		options.MaxStories = 100
	}
	if options.DeduplicationTTL <= 0 {
		options.DeduplicationTTL = 24 * time.Hour
	}
	return &Store{redis: client, options: options}
}

func (s *Store) key(value string) string { return s.options.KeyPrefix + value }

func (s *Store) collectionKey(genre string) string {
	return s.key(fmt.Sprintf(collectionKeyFmt, normalizeGenre(genre)))
}

func (s *Store) rawCollectionKey(genre string) string {
	return s.key(fmt.Sprintf(collectionKeyFmt, genre))
}

func normalizeGenre(genre string) string { return strings.ToLower(strings.TrimSpace(genre)) }

// Genres returns the set of known news genres.
func (s *Store) Genres(ctx context.Context) ([]string, error) {
	key := s.key(genresKey)
	res, err := s.redis.Raw().SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers %s: %w", key, err)
	}
	active := make([]string, 0, len(res))
	seen := make(map[string]bool, len(res))
	for _, storedGenre := range res {
		genre := normalizeGenre(storedGenre)
		if genre == "" {
			_ = s.redis.Raw().SRem(ctx, key, storedGenre).Err()
			continue
		}
		if genre != storedGenre {
			if _, err := migrateGenreScript.Run(ctx, s.redis.Raw(), []string{s.rawCollectionKey(storedGenre), s.collectionKey(genre), key}, storedGenre, genre, s.options.MaxStories).Result(); err != nil {
				return nil, fmt.Errorf("normalize genre %q: %w", storedGenre, err)
			}
		}
		if n, err := s.redis.Raw().LLen(ctx, s.collectionKey(genre)).Result(); err != nil {
			return nil, fmt.Errorf("check genre %q: %w", genre, err)
		} else if n == 0 {
			_ = s.redis.Raw().SRem(ctx, key, genre).Err()
		} else if !seen[genre] {
			active = append(active, genre)
			seen[genre] = true
		}
	}
	sort.Strings(active)
	return active, nil
}

var migrateGenreScript = goredis.NewScript(`
local items = redis.call('LRANGE', KEYS[1], 0, -1)
for _, item in ipairs(items) do redis.call('RPUSH', KEYS[2], item) end
if KEYS[1] ~= KEYS[2] then redis.call('DEL', KEYS[1]) end
redis.call('SREM', KEYS[3], ARGV[1])
if redis.call('LLEN', KEYS[2]) > 0 then
  redis.call('LTRIM', KEYS[2], -tonumber(ARGV[3]), -1)
  redis.call('SADD', KEYS[3], ARGV[2])
end
return #items
`)

// Push appends a story to the end of a genre queue (RPUSH) and records the genre.
func (s *Store) Push(ctx context.Context, genre string, story Story) error {
	genre = normalizeGenre(genre)
	if genre == "" {
		return fmt.Errorf("push: genre is required")
	}
	story.Genre = genre
	data, err := json.Marshal(story)
	if err != nil {
		return fmt.Errorf("push: marshal normalized story: %w", err)
	}
	key := s.collectionKey(genre)
	dedupeKey := s.key("news_dedupe:" + storyFingerprint(story, data))
	result, err := pushScript.Run(ctx, s.redis.Raw(), []string{key, s.key(genresKey), dedupeKey}, data, genre, s.options.MaxStories, int64(s.options.DeduplicationTTL/time.Second)).Int()
	if err != nil {
		return fmt.Errorf("rpush %s: %w", key, err)
	}
	_ = result // zero means an idempotent duplicate; it is still a successful add.
	return nil
}

var pushScript = goredis.NewScript(`
if redis.call('SET', KEYS[3], '1', 'NX', 'EX', ARGV[4]) == false then return 0 end
redis.call('RPUSH', KEYS[1], ARGV[1])
redis.call('LTRIM', KEYS[1], -tonumber(ARGV[3]), -1)
redis.call('SADD', KEYS[2], ARGV[2])
return 1
`)

func storyFingerprint(story Story, data []byte) string {
	basis := story.Genre + "\x00" + strings.TrimSpace(story.StoryID) + "\x00" + strings.TrimSpace(story.EventID)
	if basis == story.Genre+"\x00\x00" {
		basis = string(data)
	}
	sum := sha256.Sum256([]byte(basis))
	return fmt.Sprintf("%x", sum[:])
}

// Next rotates a genre queue with LMOVE (RIGHT → LEFT, same key) and returns that story.
// Equivalent to RPOPLPUSH: item moves from the tail to the head, so nothing is deleted.
// ok is false when the queue is empty.
func (s *Store) Next(ctx context.Context, genre string) (story Story, ok bool, err error) {
	key := s.collectionKey(genre)
	maxAttempts, err := s.redis.Raw().LLen(ctx, key).Result()
	if err != nil {
		return Story{}, false, fmt.Errorf("llen %s: %w", key, err)
	}
	for attempt := int64(0); attempt < maxAttempts; attempt++ {
		res, moveErr := s.redis.Raw().LMove(ctx, key, key, "RIGHT", "LEFT").Result()
		if moveErr == goredis.Nil {
			return Story{}, false, nil
		}
		if moveErr != nil {
			return Story{}, false, fmt.Errorf("lmove %s: %w", key, moveErr)
		}
		if err := json.Unmarshal([]byte(res), &story); err == nil {
			return story, true, nil
		}
		pipe := s.redis.Raw().TxPipeline()
		pipe.LRem(ctx, key, 1, res)
		deadKey := s.key("news_dead_letter")
		pipe.LPush(ctx, deadKey, res)
		pipe.LTrim(ctx, deadKey, 0, 99)
		if _, quarantineErr := pipe.Exec(ctx); quarantineErr != nil {
			return Story{}, false, fmt.Errorf("quarantine invalid news item for genre %q: %w", genre, quarantineErr)
		}
	}
	return Story{}, false, nil
}

// Len returns how many stories are queued for a genre.
func (s *Store) Len(ctx context.Context, genre string) (int64, error) {
	key := s.collectionKey(genre)
	n, err := s.redis.Raw().LLen(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("llen %s: %w", key, err)
	}
	return n, nil
}

// ByGenre returns raw news JSON strings for a genre without consuming them.
func (s *Store) ByGenre(ctx context.Context, genre string) ([]string, error) {
	key := s.collectionKey(genre)
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
