package news

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	atlasredis "github.com/aneeshpatne/atlas/internal/redis"
)

func TestRedisStoreIntegration(t *testing.T) {
	address := os.Getenv("ATLAS_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("set ATLAS_TEST_REDIS_ADDR to run Redis integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := atlasredis.New(ctx, atlasredis.Config{Address: address, DB: 15})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	prefix := "atlas-test:" + time.Now().Format("150405.000000000") + ":"
	store := NewStoreWithOptions(client, StoreOptions{KeyPrefix: prefix, MaxStories: 2, DeduplicationTTL: time.Hour})

	first := Story{StoryID: "one", Title: "First", Genre: " India "}
	if err := store.Push(ctx, first.Genre, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Push(ctx, first.Genre, first); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Len(ctx, "india"); err != nil || n != 1 {
		t.Fatalf("deduplicated length = %d, %v; want 1", n, err)
	}
	for _, id := range []string{"two", "three"} {
		if err := store.Push(ctx, "INDIA", Story{StoryID: id, Title: id, Genre: "INDIA"}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := store.Len(ctx, "india"); err != nil || n != 2 {
		t.Fatalf("bounded length = %d, %v; want 2", n, err)
	}
	if genres, err := store.Genres(ctx); err != nil || !reflect.DeepEqual(genres, []string{"india"}) {
		t.Fatalf("genres = %v, %v", genres, err)
	}

	if err := client.Raw().RPush(ctx, store.collectionKey("india"), "not-json").Err(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Next(ctx, "india"); err != nil || !ok {
		t.Fatalf("Next after poison item = ok %v, err %v", ok, err)
	}
	if n, err := client.Raw().LLen(ctx, store.key("news_dead_letter")).Result(); err != nil || n != 1 {
		t.Fatalf("dead-letter length = %d, %v; want 1", n, err)
	}

	legacy := Story{StoryID: "legacy", Title: "Legacy", Genre: "World"}
	data, _ := json.Marshal(legacy)
	if err := client.Raw().RPush(ctx, store.rawCollectionKey("World"), data).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Raw().SAdd(ctx, store.key(genresKey), "World").Err(); err != nil {
		t.Fatal(err)
	}
	genres, err := store.Genres(ctx)
	if err != nil || !reflect.DeepEqual(genres, []string{"india", "world"}) {
		t.Fatalf("migrated genres = %v, %v", genres, err)
	}
	if n := client.Raw().LLen(ctx, store.rawCollectionKey("World")).Val(); n != 0 {
		t.Fatalf("legacy queue still has %d items", n)
	}
}
