package screennews

import (
	"context"

	screenv1 "github.com/aneeshpatne/atlas/gen/screen/v1"
	"github.com/aneeshpatne/atlas/internal/news"
)

type Repository interface {
	Add(context.Context, *screenv1.NewsItem) error
}

type Redis struct{ store *news.Store }

func NewRedis(store *news.Store) *Redis { return &Redis{store: store} }
func (r *Redis) Add(ctx context.Context, item *screenv1.NewsItem) error {
	return r.store.Push(ctx, item.Genre, toStory(item))
}
func toStory(item *screenv1.NewsItem) news.Story {
	sources := make([]news.Source, len(item.Sources))
	for i, s := range item.Sources {
		sources[i] = news.Source{URL: s.Url, Domain: s.Domain, OGURL: s.OgUrl}
	}
	return news.Story{StoryID: item.StoryId, EventID: item.EventId, Title: item.Title, Description: item.Description, Genre: item.Genre, Sources: sources}
}
