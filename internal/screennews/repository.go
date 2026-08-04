package screennews

import (
	"context"
	"sync"

	screenv1 "github.com/aneeshpatne/atlas/gen/screen/v1"
	"github.com/aneeshpatne/atlas/internal/news"
)

type Repository interface {
	Add(context.Context, *screenv1.NewsItem) error
	List(context.Context) ([]*screenv1.NewsItem, error)
}

type Memory struct {
	mu    sync.RWMutex
	items []*screenv1.NewsItem
}

func (m *Memory) Add(_ context.Context, item *screenv1.NewsItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = append(m.items, item)
	return nil
}
func (m *Memory) List(_ context.Context) ([]*screenv1.NewsItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]*screenv1.NewsItem(nil), m.items...), nil
}

type Redis struct{ store *news.Store }

func NewRedis(store *news.Store) *Redis { return &Redis{store: store} }
func (r *Redis) Add(ctx context.Context, item *screenv1.NewsItem) error {
	return r.store.Push(ctx, item.Genre, toStory(item))
}
func (r *Redis) List(ctx context.Context) ([]*screenv1.NewsItem, error) {
	genres, err := r.store.Genres(ctx)
	if err != nil {
		return nil, err
	}
	var out []*screenv1.NewsItem
	for _, genre := range genres {
		stories, err := r.store.StoriesByGenre(ctx, genre)
		if err != nil {
			return nil, err
		}
		for _, story := range stories {
			out = append(out, toProto(story))
		}
	}
	return out, nil
}
func toStory(item *screenv1.NewsItem) news.Story {
	sources := make([]news.Source, len(item.Sources))
	for i, s := range item.Sources {
		sources[i] = news.Source{URL: s.Url, Domain: s.Domain, OGURL: s.OgUrl}
	}
	return news.Story{StoryID: item.StoryId, EventID: item.EventId, Title: item.Title, Description: item.Description, Genre: item.Genre, Sources: sources}
}
func toProto(item news.Story) *screenv1.NewsItem {
	sources := make([]*screenv1.Source, len(item.Sources))
	for i, s := range item.Sources {
		sources[i] = &screenv1.Source{Url: s.URL, Domain: s.Domain, OgUrl: s.OGURL}
	}
	return &screenv1.NewsItem{StoryId: item.StoryID, EventId: item.EventID, Title: item.Title, Description: item.Description, Genre: item.Genre, Sources: sources}
}
