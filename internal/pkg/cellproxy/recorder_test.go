package cellproxy

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorderTracksTrafficPerDestination(t *testing.T) {
	t.Parallel()

	rec := NewRecorder(10)
	keyA := rec.Open("a.example:443")
	keyB := rec.Open("b.example:80")
	rec.Up(keyA, 100)
	rec.Down(keyA, 900)
	rec.Up(keyB, 10)
	rec.Down(keyB, 20)
	rec.Close(keyA)

	snap := rec.Snapshot()
	assert.EqualValues(t, 2, snap.Requests)
	assert.EqualValues(t, 110, snap.BytesUp)
	assert.EqualValues(t, 920, snap.BytesDown)
	assert.EqualValues(t, 1, snap.Active)

	require.Len(t, snap.Destinations, 2)
	assert.Equal(t, "a.example:443", snap.Destinations[0].Destination)
	assert.EqualValues(t, 100, snap.Destinations[0].BytesUp)
	assert.EqualValues(t, 900, snap.Destinations[0].BytesDown)
	assert.EqualValues(t, 0, snap.Destinations[0].Active)
	assert.EqualValues(t, 1, snap.Destinations[1].Active)
}

func TestRecorderCountsDialFailures(t *testing.T) {
	t.Parallel()

	rec := NewRecorder(10)
	rec.DialFailed()
	rec.DialFailed()

	assert.EqualValues(t, 2, rec.Snapshot().DialFailures)
}

func TestRecorderRanksAndBoundsDestinations(t *testing.T) {
	t.Parallel()

	rec := NewRecorder(2)
	for _, tc := range []struct {
		dest  string
		bytes int64
	}{
		{"low:1", 1},
		{"high:1", 1000},
		{"mid:1", 500},
	} {
		rec.Down(rec.Open(tc.dest), tc.bytes)
	}

	snap := rec.Snapshot()
	require.Len(t, snap.Destinations, 2)
	assert.Equal(t, "high:1", snap.Destinations[0].Destination)
	assert.Equal(t, "mid:1", snap.Destinations[1].Destination)
	assert.EqualValues(t, 3, snap.Requests)
}

func TestRecorderFoldsPastCapIntoOverflow(t *testing.T) {
	t.Parallel()

	rec := NewRecorder(0)
	total := maxTrackedDestinations + 5
	for i := range total {
		rec.Up(rec.Open(fmt.Sprintf("dest-%d:1", i)), 1)
	}

	snap := rec.Snapshot()
	assert.EqualValues(t, total, snap.Requests)

	overflow := false
	for _, dest := range snap.Destinations {
		if dest.Destination == overflowDestination {
			overflow = true
		}
	}
	assert.True(t, overflow, "destinations past the cap should fold into the overflow bucket")
}
