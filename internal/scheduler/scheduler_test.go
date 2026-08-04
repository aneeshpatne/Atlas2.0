package scheduler

import (
	"testing"
	"time"
)

func TestIsInsideActiveWindow(t *testing.T) {
	loc := time.FixedZone("test", 19800)
	tests := []struct {
		at   string
		want bool
	}{{"06:59:59", false}, {"07:00:00", true}, {"07:00:01", true}, {"22:59:59", true}, {"23:00:00", false}, {"23:00:01", false}}
	for _, tt := range tests {
		t.Run(tt.at, func(t *testing.T) {
			v, _ := time.ParseInLocation("15:04:05", tt.at, loc)
			if got := IsInsideActiveWindow(v, 7, 0, 23, 0); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestOvernightWindow(t *testing.T) {
	for _, tt := range []struct {
		hour int
		want bool
	}{{21, false}, {22, true}, {2, true}, {6, false}} {
		at := time.Date(2026, 1, 1, tt.hour, 0, 0, 0, time.UTC)
		if got := IsInsideActiveWindow(at, 22, 0, 6, 0); got != tt.want {
			t.Errorf("hour %d got %v", tt.hour, got)
		}
	}
}

func TestNewsPassCronSpec(t *testing.T) {
	tests := []struct {
		interval time.Duration
		want     string
		ok       bool
	}{
		{15 * time.Minute, "0,15,30,45 * * * *", true},
		{30 * time.Minute, "0,30 * * * *", true},
		{time.Hour, "0 * * * *", true},
		{10 * time.Minute, "0,10,20,30,40,50 * * * *", true},
		{time.Minute, "0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59 * * * *", true},
		{20 * time.Minute, "0,20,40 * * * *", true},
		{7 * time.Minute, "", false},
		{90 * time.Minute, "", false},
		{0, "", false},
		{90 * time.Second, "", false},
	}
	for _, tt := range tests {
		got, err := NewsPassCronSpec(tt.interval)
		if tt.ok {
			if err != nil {
				t.Fatalf("interval %v: unexpected err %v", tt.interval, err)
			}
			if got != tt.want {
				t.Fatalf("interval %v: got %q want %q", tt.interval, got, tt.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("interval %v: expected error, got %q", tt.interval, got)
		}
	}
}

type recordingCommander struct {
	starts, stops, passes int
}

func (r *recordingCommander) RequestScheduledStart()    { r.starts++ }
func (r *recordingCommander) RequestScheduledShutdown() { r.stops++ }
func (r *recordingCommander) RequestNewsPass()          { r.passes++ }

func TestNewRegistersJobs(t *testing.T) {
	target := &recordingCommander{}
	c, err := New(time.UTC, 7, 0, 23, 0, 15*time.Minute, target)
	if err != nil {
		t.Fatal(err)
	}
	entries := c.Entries()
	if len(entries) != 3 {
		t.Fatalf("got %d entries want 3", len(entries))
	}
}

func TestNewRejectsBadInterval(t *testing.T) {
	_, err := New(time.UTC, 7, 0, 23, 0, 7*time.Minute, &recordingCommander{})
	if err == nil {
		t.Fatal("expected error")
	}
}
