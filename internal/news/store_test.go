package news

import (
	"encoding/json"
	"testing"
)

func TestStoryJSONRoundTrip(t *testing.T) {
	raw := `{
		"storyId": "s1",
		"eventId": "e1",
		"title": "Hello",
		"description": "World",
		"genre": "India",
		"sources": [{"url": "https://example.com", "domain": "example", "ogurl": "https://img"}]
	}`
	var story Story
	if err := json.Unmarshal([]byte(raw), &story); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if story.Title != "Hello" || story.Genre != "India" {
		t.Fatalf("unexpected story: %+v", story)
	}
	if len(story.Sources) != 1 || story.Sources[0].Domain != "example" {
		t.Fatalf("unexpected sources: %+v", story.Sources)
	}
}
