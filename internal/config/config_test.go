package config

import (
	"testing"
	"time"
)

func TestDefaultValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestValidation(t *testing.T) {
	tests := []func(*Config){
		func(c *Config) { c.Timezone = "Mars/Olympus" },
		func(c *Config) { c.NewsRefreshInterval = 0 },
		func(c *Config) { c.NewsRefreshInterval = 7 * time.Minute },
		func(c *Config) { c.NewsRefreshInterval = 90 * time.Minute },
		func(c *Config) { c.AlertDisplayDuration = 0 },
		func(c *Config) { c.AlertQueueCapacity = 0 },
		func(c *Config) { c.StartHour = 24 },
		func(c *Config) { c.StartHour, c.StartMinute, c.StopHour, c.StopMinute = 7, 0, 7, 0 },
	}
	for i, mutate := range tests {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("case %d unexpectedly valid", i)
		}
	}
}
