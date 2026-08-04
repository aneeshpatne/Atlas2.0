package redis

// Config holds connection settings for a Redis server.
type Config struct {
	Address  string
	Password string
	DB       int
}
