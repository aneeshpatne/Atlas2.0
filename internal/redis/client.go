// Package redis provides a thin Redis client wrapper.
package redis

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// Client is a thin wrapper around go-redis.
type Client struct {
	raw *goredis.Client
}

// New connects to Redis and pings once to verify the connection.
func New(ctx context.Context, config Config) (*Client, error) {
	raw := goredis.NewClient(&goredis.Options{
		Addr:     config.Address,
		Password: config.Password,
		DB:       config.DB,
	})
	if err := raw.Ping(ctx).Err(); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("connect to redis: %w", err)
	}
	return &Client{raw: raw}, nil
}

// Raw returns the underlying go-redis client for advanced use.
func (c *Client) Raw() *goredis.Client {
	return c.raw
}

// Close closes the Redis connection.
func (c *Client) Close() error {
	return c.raw.Close()
}

// Ping checks that Redis is reachable.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.raw.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}
	return nil
}
