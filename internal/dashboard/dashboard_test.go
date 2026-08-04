package dashboard

import (
	"testing"

	"github.com/aneeshpatne/atlas/internal/kindle"
	"github.com/aneeshpatne/atlas/internal/news"
)

func TestToKindleStoryMapsFields(t *testing.T) {
	got := toKindleStory(news.Story{
		StoryID:     "s1",
		EventID:     "e1",
		Title:       "Title",
		Description: "Desc",
		Genre:       "India",
		Sources: []news.Source{{
			URL:    "https://example.com",
			Domain: "example",
			OGURL:  "https://img",
		}},
	})
	want := kindle.Story{
		StoryID:     "s1",
		EventID:     "e1",
		Title:       "Title",
		Description: "Desc",
		Genre:       "India",
		Sources: []kindle.StorySource{{
			URL:    "https://example.com",
			Domain: "example",
			OGURL:  "https://img",
		}},
	}
	if got.StoryID != want.StoryID || got.Title != want.Title || got.Genre != want.Genre {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(got.Sources) != 1 || got.Sources[0].Domain != "example" || got.Sources[0].OGURL != "https://img" {
		t.Fatalf("sources = %+v", got.Sources)
	}
}
