package pr0xteus

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/psyb0t/ctxerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The collectors under test here are package-level promauto singletons
// (metrics.go), shared across the whole test binary. None of these tests
// call t.Parallel(): asserting an absolute value or an unsynchronized
// before/after delta against a shared counter would be flaky under real
// parallel execution. Uniquely named pools per test/subtest keep counter and
// gauge label series from colliding with any other test in this package.

// counterValue reads the current value of one label combination of a
// CounterVec without pulling in prometheus/client_golang/testutil (not
// vendored in this repo).
func counterValue(t *testing.T, vec *prometheus.CounterVec, labelValues ...string) float64 {
	t.Helper()

	metric := &dto.Metric{}
	require.NoError(t, vec.WithLabelValues(labelValues...).Write(metric))

	return metric.GetCounter().GetValue()
}

// gaugeValue reads the current value of one label combination of a GaugeVec.
func gaugeValue(t *testing.T, vec *prometheus.GaugeVec, labelValues ...string) float64 {
	t.Helper()

	metric := &dto.Metric{}
	require.NoError(t, vec.WithLabelValues(labelValues...).Write(metric))

	return metric.GetGauge().GetValue()
}

// histogramSampleCount reads the observation count of one label combination
// of a HistogramVec.
func histogramSampleCount(t *testing.T, vec *prometheus.HistogramVec, labelValues ...string) uint64 {
	t.Helper()

	observer := vec.WithLabelValues(labelValues...)

	writer, ok := observer.(prometheus.Metric)
	require.True(t, ok, "histogram observer must also implement prometheus.Metric")

	metric := &dto.Metric{}
	require.NoError(t, writer.Write(metric))

	return metric.GetHistogram().GetSampleCount()
}

func TestManager_SpawnMetrics_RecordOutcomeAndDuration(t *testing.T) {
	testCases := []struct {
		name        string
		poolName    string
		spawnErr    error
		wantOutcome string
	}{
		{
			name:        "success",
			poolName:    "metrics-spawn-success",
			wantOutcome: metricOutcomeSuccess,
		},
		{
			name:        "generic spawn failure",
			poolName:    "metrics-spawn-fail",
			spawnErr:    ctxerrors.New("provider rejected config"),
			wantOutcome: metricOutcomeSpawnFail,
		},
		{
			name:        "spawn timeout",
			poolName:    "metrics-spawn-timeout",
			spawnErr:    ctxerrors.Wrap(ErrSpawnTimeout, "container never reported running"),
			wantOutcome: metricOutcomeTimeout,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			spawner := &runtimeTestSpawner{}
			if tc.spawnErr != nil {
				spawner.spawnErrors = map[string]error{
					tc.poolName + ":conf-a": tc.spawnErr,
				}
			}

			manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
				tc.poolName: runtimePoolSpec(tc.poolName, "conf-a"),
			}, map[string]string{})

			before := counterValue(t, TunnelSpawnsTotal, tc.poolName, tc.wantOutcome)
			durationBefore := histogramSampleCount(t, TunnelSpawnDuration, tc.poolName)

			_, err := manager.AcquireForPool(context.Background(), tc.poolName, nil, false)

			after := counterValue(t, TunnelSpawnsTotal, tc.poolName, tc.wantOutcome)
			durationAfter := histogramSampleCount(t, TunnelSpawnDuration, tc.poolName)

			assert.Equal(t, before+1, after)

			if tc.spawnErr != nil {
				require.Error(t, err)
				assert.Equal(
					t, durationBefore, durationAfter,
					"a failed spawn must not record a duration sample",
				)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, durationBefore+1, durationAfter)
		})
	}
}

func TestPoolState_SetTunnelUpdatesHotGauge(t *testing.T) {
	const poolName = "metrics-hot-gauge"

	state := NewPoolState(runtimePoolSpec(poolName, "conf-a"))

	state.setTunnel(&Tunnel{ConfName: "conf-a", State: TunnelStateHot})
	assert.InDelta(t, 1, gaugeValue(t, TunnelHotGauge, poolName), 0)

	state.setTunnel(nil)
	assert.InDelta(t, 0, gaugeValue(t, TunnelHotGauge, poolName), 0)
}

func TestReaper_KillTunnelIncrementsReapsMetric(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)

	testCases := []struct {
		name     string
		poolName string
		reason   string
		tunnel   *Tunnel
		run      func(*Reaper)
	}{
		{
			name:     "idle reap",
			poolName: "metrics-reap-idle",
			reason:   "idle",
			tunnel: &Tunnel{
				ContainerID: "idle-cell",
				ConfName:    "de-berlin",
				State:       TunnelStateHot,
				LastUsedAt:  now.Add(-2 * time.Minute),
				HealthyAt:   now,
			},
			run: func(r *Reaper) { r.reapIdle(context.Background()) },
		},
		{
			name:     "unhealthy reap",
			poolName: "metrics-reap-unhealthy",
			reason:   "unhealthy",
			tunnel: &Tunnel{
				ContainerID: "stale-cell",
				ConfName:    "pl-warsaw",
				State:       TunnelStateHot,
				LastUsedAt:  now,
				HealthyAt:   now.Add(-2 * time.Minute),
			},
			run: func(r *Reaper) { r.checkHealth(context.Background()) },
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			spawner := &runtimeTestSpawner{}
			manager := newRuntimeTestManager(spawner, map[string]PoolSpec{
				tc.poolName: runtimePoolSpec(tc.poolName, tc.tunnel.ConfName),
			}, map[string]string{})
			manager.Pools()[tc.poolName].setTunnel(tc.tunnel)

			reaper := NewReaper(
				Config{
					IdleTimeout:           time.Minute,
					HealthHandshakeMaxAge: time.Minute,
					FailureCacheTTL:       time.Minute,
				},
				manager, spawner, nil,
			)
			reaper.nowFn = func() time.Time { return now }

			before := counterValue(t, TunnelReapsTotal, tc.poolName, tc.reason)
			tc.run(reaper)
			after := counterValue(t, TunnelReapsTotal, tc.poolName, tc.reason)

			assert.Equal(t, before+1, after)
			assert.Nil(t, manager.Pools()[tc.poolName].Snapshot())
		})
	}
}
