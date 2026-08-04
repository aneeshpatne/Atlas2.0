package config

import (
	"fmt"
	"time"
)

type Config struct {
	Timezone                       string
	StartHour, StartMinute         int
	StopHour, StopMinute           int
	NewsRefreshInterval            time.Duration
	AlertDisplayDuration           time.Duration
	WorkerStopTimeout              time.Duration
	OperationTimeout               time.Duration
	StartupRetryCount              int
	StartupRetryDelay              time.Duration
	GRPCListenAddress              string
	GRPCShutdownTimeout            time.Duration
	AlertQueueCapacity             int
	CommandQueueCapacity           int
	AlertMessageMaxBytes           int
	ShutdownDisplayOnServiceExit   bool
	AllowAlertsOutsideActiveWindow bool
}

func Default() Config {
	return Config{Timezone: "Asia/Kolkata", StartHour: 7, StopHour: 23,
		NewsRefreshInterval: 15 * time.Minute, AlertDisplayDuration: 30 * time.Second,
		WorkerStopTimeout: 5 * time.Second, OperationTimeout: 30 * time.Second,
		StartupRetryCount: 3, StartupRetryDelay: time.Second,
		GRPCListenAddress: ":50050", GRPCShutdownTimeout: 10 * time.Second,
		AlertQueueCapacity: 100, CommandQueueCapacity: 256, AlertMessageMaxBytes: 1000}
}

func (c Config) Validate() error {
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	if c.StartHour < 0 || c.StartHour > 23 || c.StopHour < 0 || c.StopHour > 23 {
		return fmt.Errorf("hours must be between 0 and 23")
	}
	if c.StartMinute < 0 || c.StartMinute > 59 || c.StopMinute < 0 || c.StopMinute > 59 {
		return fmt.Errorf("minutes must be between 0 and 59")
	}
	if c.StartHour == c.StopHour && c.StartMinute == c.StopMinute {
		return fmt.Errorf("start and stop times must differ")
	}
	if c.NewsRefreshInterval <= 0 || c.AlertDisplayDuration <= 0 {
		return fmt.Errorf("refresh and alert durations must be positive")
	}
	if c.NewsRefreshInterval > time.Hour {
		return fmt.Errorf("news refresh interval must be at most 1 hour")
	}
	if c.NewsRefreshInterval%time.Minute != 0 {
		return fmt.Errorf("news refresh interval must be a whole number of minutes")
	}
	if 60%(int(c.NewsRefreshInterval/time.Minute)) != 0 {
		return fmt.Errorf("news refresh interval must divide 1 hour evenly (e.g. 15m for :00/:15/:30/:45)")
	}
	if c.WorkerStopTimeout <= 0 || c.OperationTimeout <= 0 || c.GRPCShutdownTimeout <= 0 {
		return fmt.Errorf("timeouts must be positive")
	}
	if c.StartupRetryCount <= 0 || c.StartupRetryDelay <= 0 {
		return fmt.Errorf("startup retry settings must be positive")
	}
	if c.AlertQueueCapacity <= 0 || c.CommandQueueCapacity <= 0 || c.AlertMessageMaxBytes <= 0 {
		return fmt.Errorf("queue capacities and message limit must be positive")
	}
	if c.GRPCListenAddress == "" {
		return fmt.Errorf("gRPC listen address is required")
	}
	return nil
}
