package lifecycle

import (
	"testing"
	"time"
)

func TestExpectedDeathAligned(t *testing.T) {
	boot := time.Date(2026, 4, 16, 14, 17, 33, 0, time.UTC)
	c := NewAt(boot)
	c.SetNow(func() time.Time { return boot })

	want := time.Date(2026, 4, 16, 15, 0, 0, 0, time.UTC)
	if got := c.ExpectedDeathAt(); !got.Equal(want) {
		t.Fatalf("ExpectedDeathAt = %v, want %v", got, want)
	}
}

func TestExpectedDeathExactlyOnTheHour(t *testing.T) {
	// At exactly 15:00:00, next death is 16:00:00 (not 15:00:00).
	boot := time.Date(2026, 4, 16, 15, 0, 0, 0, time.UTC)
	c := NewAt(boot)
	c.SetNow(func() time.Time { return boot })

	want := time.Date(2026, 4, 16, 16, 0, 0, 0, time.UTC)
	if got := c.ExpectedDeathAt(); !got.Equal(want) {
		t.Fatalf("ExpectedDeathAt = %v, want %v", got, want)
	}
}

func TestRemainingLifespan(t *testing.T) {
	boot := time.Date(2026, 4, 16, 14, 30, 0, 0, time.UTC)
	c := NewAt(boot)
	c.SetNow(func() time.Time { return boot })

	if got := c.RemainingLifespan(); got != 30*time.Minute {
		t.Fatalf("RemainingLifespan = %v, want 30m", got)
	}
}

func TestRemainingLifespanClampsAtZero(t *testing.T) {
	boot := time.Date(2026, 4, 16, 14, 30, 0, 0, time.UTC)
	c := NewAt(boot)
	// Pretend we're running 10 minutes past expected death.
	c.SetNow(func() time.Time {
		return time.Date(2026, 4, 16, 15, 10, 0, 0, time.UTC)
	})

	if got := c.RemainingLifespan(); got != 0 {
		t.Fatalf("RemainingLifespan past death = %v, want 0", got)
	}
}

func TestLifeDuration(t *testing.T) {
	boot := time.Date(2026, 4, 16, 14, 30, 0, 0, time.UTC)
	c := NewAt(boot)
	c.SetNow(func() time.Time {
		return time.Date(2026, 4, 16, 14, 37, 12, 0, time.UTC)
	})

	if got := c.LifeDuration(); got != 7*time.Minute+12*time.Second {
		t.Fatalf("LifeDuration = %v, want 7m12s", got)
	}
}

func TestUnalignedInterval(t *testing.T) {
	t.Setenv("DEPLOY_INTERVAL_SECONDS", "600")
	boot := time.Date(2026, 4, 16, 14, 17, 33, 0, time.UTC)
	c := NewAt(boot)
	c.SetNow(func() time.Time { return boot })

	if c.Aligned() {
		t.Fatalf("expected unaligned with 600s interval")
	}
	want := boot.Add(10 * time.Minute)
	if got := c.ExpectedDeathAt(); !got.Equal(want) {
		t.Fatalf("ExpectedDeathAt = %v, want %v", got, want)
	}
}
