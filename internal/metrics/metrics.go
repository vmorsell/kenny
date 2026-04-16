// Package metrics registers Kenny's Prometheus instruments against a registry.
// Lifespan and counts sourced from SQLite are exposed as GaugeFuncs so they
// reflect the authoritative persistent state rather than this process's
// in-memory view.
package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vmorsell/kenny/internal/lifecycle"
	"github.com/vmorsell/kenny/internal/state"
)

type BuildInfo struct {
	SHA     string
	BuiltAt string
}

type Metrics struct {
	ClaudeInvocations *prometheus.CounterVec
	ClaudeDuration    prometheus.Histogram
	SelfModCommits    prometheus.Counter
}

// Register builds and registers all of Kenny's metrics against reg.
// Returns a Metrics handle for incrementing counters; gauges are driven
// by the Clock and Store directly.
func Register(reg prometheus.Registerer, clock *lifecycle.Clock, store *state.Store, info BuildInfo) *Metrics {
	m := &Metrics{
		ClaudeInvocations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kenny_claude_invocations_total",
				Help: "Count of claude -p invocations by result.",
			},
			[]string{"result"},
		),
		ClaudeDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "kenny_claude_invocation_duration_seconds",
				Help:    "Wall-clock duration of claude -p invocations.",
				Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s … ~68m
			},
		),
		SelfModCommits: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "kenny_self_mod_commits_total",
				Help: "Count of self-modification commits Kenny has pushed this life.",
			},
		),
	}
	reg.MustRegister(m.ClaudeInvocations, m.ClaudeDuration, m.SelfModCommits)

	reg.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "kenny_life_duration_seconds",
			Help: "Seconds since the start of this life.",
		},
		func() float64 { return clock.LifeDuration().Seconds() },
	))
	reg.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "kenny_time_to_death_seconds",
			Help: "Seconds remaining until expected SIGTERM at the top of the hour.",
		},
		func() float64 { return clock.RemainingLifespan().Seconds() },
	))
	reg.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "kenny_life_count_total",
			Help: "Monotonically increasing boot counter read from persistent state.",
		},
		func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			v, ok, err := store.GetMetadata(ctx, "boot_count")
			if err != nil || !ok {
				return 0
			}
			var n float64
			if _, err := fmt.Sscanf(v, "%f", &n); err != nil {
				return 0
			}
			return n
		},
	))
	reg.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "kenny_journal_entries_total",
			Help: "Total journal entries persisted across all lives.",
		},
		func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			n, err := store.CountJournalEntries(ctx)
			if err != nil {
				return 0
			}
			return float64(n)
		},
	))
	reg.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "kenny_inflight_tasks",
			Help: "Open inflight tasks surviving across rebirths.",
		},
		func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			n, err := store.CountInflight(ctx)
			if err != nil {
				return 0
			}
			return float64(n)
		},
	))

	bi := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kenny_build_info",
			Help: "Build information: value is always 1, metadata is in labels.",
		},
		[]string{"sha", "built_at"},
	)
	bi.WithLabelValues(info.SHA, info.BuiltAt).Set(1)
	reg.MustRegister(bi)

	return m
}
