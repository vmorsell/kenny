// Package lifecycle reports how much of Kenny's current life remains.
// Coolify redeploys Kenny at the top of every hour, so death is
// expected at the next :00. If DEPLOY_INTERVAL_SECONDS is overridden
// to something other than 3600, the clock falls back to boot+interval.
package lifecycle

import (
	"os"
	"strconv"
	"time"
)

type Clock struct {
	bootAt   time.Time
	interval time.Duration
	aligned  bool
	now      func() time.Time
}

// New constructs a Clock rooted at the current moment, reading
// DEPLOY_INTERVAL_SECONDS from the environment if present.
func New() *Clock {
	return NewAt(time.Now().UTC())
}

// NewAt constructs a Clock rooted at a specific boot time. Useful for tests.
func NewAt(boot time.Time) *Clock {
	interval := time.Hour
	aligned := true
	if v := os.Getenv("DEPLOY_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Second
			aligned = interval == time.Hour
		}
	}
	return &Clock{
		bootAt:   boot,
		interval: interval,
		aligned:  aligned,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetNow overrides the clock's time source. Tests only.
func (c *Clock) SetNow(f func() time.Time) { c.now = f }

func (c *Clock) BootAt() time.Time     { return c.bootAt }
func (c *Clock) Interval() time.Duration { return c.interval }
func (c *Clock) Aligned() bool         { return c.aligned }

// ExpectedDeathAt returns the moment Kenny expects to be killed —
// a fixed point anchored at boot. In aligned mode this is the first
// top-of-hour strictly after boot; otherwise boot+interval.
func (c *Clock) ExpectedDeathAt() time.Time {
	if c.aligned {
		next := c.bootAt.Truncate(time.Hour).Add(time.Hour)
		if !next.After(c.bootAt) {
			next = next.Add(time.Hour)
		}
		return next
	}
	return c.bootAt.Add(c.interval)
}

// LifeDuration is how long this life has been running.
func (c *Clock) LifeDuration() time.Duration {
	return c.now().Sub(c.bootAt)
}

// RemainingLifespan is how much time is left until expected death,
// clamped at zero.
func (c *Clock) RemainingLifespan() time.Duration {
	d := c.ExpectedDeathAt().Sub(c.now())
	if d < 0 {
		return 0
	}
	return d
}
