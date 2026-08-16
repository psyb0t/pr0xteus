package pr0xteus

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	metricNamespace    = ServiceName
	metricSubsystem    = "tunnel_pool"
	metricLabelPool    = "pool"
	metricLabelOutcome = "outcome"
	metricLabelReason  = "reason"

	// Spawn outcomes for TunnelSpawnsTotal.
	metricOutcomeSuccess   = "success"
	metricOutcomeTimeout   = "timeout"
	metricOutcomeSpawnFail = "spawn_fail"

	// Acquire outcome for TunnelAcquireTotal (success path).
	metricOutcomeOK = "ok"
)

// Prometheus collectors keep every label bounded by configuration: pool,
// state, reason, and outcome are closed sets.
//
//nolint:gochecknoglobals // promauto registers at package-init by design
var (
	// TunnelSpawnsTotal counts cell container spawn attempts.
	// `pool` is the logical pool name, `outcome` is one of
	// "success", "timeout", "spawn_fail".
	TunnelSpawnsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: metricSubsystem,
		Name:      "spawns_total",
		Help:      "cell container spawns by pool + outcome",
	}, []string{metricLabelPool, metricLabelOutcome})

	// TunnelReapsTotal counts reaper-driven container kills.
	// `reason` is "idle" or "unhealthy".
	TunnelReapsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: metricSubsystem,
		Name:      "reaps_total",
		Help:      "tunnel reaps by pool + reason",
	}, []string{metricLabelPool, metricLabelReason})

	// TunnelAcquireTotal counts proxy assignments. `pool` is the
	// pool the assignment ultimately came from (which may be the
	// fallback pool, not the primary). `outcome` is "ok",
	// "exhausted", "unavailable", "invalid_country", "unknown_pool".
	TunnelAcquireTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: metricSubsystem,
		Name:      "acquires_total",
		Help:      "proxy acquisitions by pool and outcome",
	}, []string{metricLabelPool, metricLabelOutcome})

	// TunnelHotGauge is the current count of hot tunnels per pool.
	TunnelHotGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Subsystem: metricSubsystem,
		Name:      "hot_tunnels",
		Help:      "count of hot tunnels currently in the pool",
	}, []string{metricLabelPool})

	// TunnelSpawnDuration measures docker-create + health-probe
	// total elapsed.
	TunnelSpawnDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: metricSubsystem,
		Name:      "spawn_seconds",
		Help:      "duration of a tunnel spawn from docker-create to hot",
		Buckets:   []float64{1, 2, 3, 5, 8, 13, 20, 30},
	}, []string{metricLabelPool})
)
