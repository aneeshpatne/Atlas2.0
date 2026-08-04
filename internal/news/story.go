package news

// Story is a news event stored in Redis and rendered by story mode.
type Story struct {
	StoryID     string   `json:"storyId"`
	EventID     string   `json:"eventId"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Genre       string   `json:"genre"`
	Sources     []Source `json:"sources"`
}

// Source is an origin link (and optional image) for a story.
type Source struct {
	URL    string `json:"url"`
	Domain string `json:"domain"`
	OGURL  string `json:"ogurl"`
}
