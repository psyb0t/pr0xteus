// Package cellproxy is the metrics-emitting SOCKS5 proxy that runs inside a
// pr0xteus cell in place of microsocks. It proxies CONNECT through the cell's
// WireGuard default route while recording per-destination request counts, byte
// volumes, and live-connection counts, and exposes them plus a real liveness
// signal over a control HTTP server the controller scrapes.
package cellproxy

import (
	"sort"
	"sync"
)

// maxTrackedDestinations bounds the per-destination map so a caller reaching
// many unique hosts cannot grow cell memory without limit. Destinations beyond
// the cap fold into a single overflow bucket.
const maxTrackedDestinations = 2048

// overflowDestination is the bucket that absorbs destinations past the cap.
const overflowDestination = "(other)"

// DestStat aggregates traffic for one destination host:port.
type DestStat struct {
	Destination string `json:"destination"`
	Requests    int64  `json:"requests"`
	BytesUp     int64  `json:"bytesUp"`
	BytesDown   int64  `json:"bytesDown"`
	Active      int64  `json:"active"`
}

// Stats is the snapshot the control server returns from GET /stats. Totals are
// exact; Destinations is a bounded, byte-volume-ranked slice.
type Stats struct {
	Requests     int64      `json:"requests"`
	BytesUp      int64      `json:"bytesUp"`
	BytesDown    int64      `json:"bytesDown"`
	Active       int64      `json:"active"`
	DialFailures int64      `json:"dialFailures"`
	Destinations []DestStat `json:"destinations"`
}

// Recorder accumulates proxy traffic metrics. It is safe for concurrent use.
type Recorder struct {
	mu           sync.Mutex
	dests        map[string]*DestStat
	requests     int64
	bytesUp      int64
	bytesDown    int64
	active       int64
	dialFailures int64
	topN         int
}

// NewRecorder returns an empty Recorder that ranks up to topN destinations in a
// snapshot. A non-positive topN falls back to the destination cap.
func NewRecorder(topN int) *Recorder {
	if topN <= 0 {
		topN = maxTrackedDestinations
	}

	return &Recorder{
		dests: make(map[string]*DestStat),
		topN:  topN,
	}
}

// Open records a new proxied request to dest and returns the bucket key later
// passed to Up, Down, and Close (the caller may have been folded into the
// overflow bucket).
func (r *Recorder) Open(dest string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	stat := r.bucket(dest)
	stat.Requests++
	stat.Active++
	r.requests++
	r.active++

	return stat.Destination
}

// Close marks one live connection to key finished.
func (r *Recorder) Close(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if stat, ok := r.dests[key]; ok && stat.Active > 0 {
		stat.Active--
	}

	if r.active > 0 {
		r.active--
	}
}

// Up records n bytes sent from the client toward key.
func (r *Recorder) Up(key string, n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if stat, ok := r.dests[key]; ok {
		stat.BytesUp += n
	}

	r.bytesUp += n
}

// Down records n bytes received from key toward the client.
func (r *Recorder) Down(key string, n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if stat, ok := r.dests[key]; ok {
		stat.BytesDown += n
	}

	r.bytesDown += n
}

// DialFailed records a failed outbound dial, a signal the controller uses to
// spot a dead tunnel without needing privileged handshake inspection.
func (r *Recorder) DialFailed() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.dialFailures++
}

// Snapshot returns the current totals plus the byte-volume-ranked, bounded
// destination breakdown.
func (r *Recorder) Snapshot() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()

	list := make([]DestStat, 0, len(r.dests))
	for _, stat := range r.dests {
		list = append(list, *stat)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].BytesUp+list[i].BytesDown > list[j].BytesUp+list[j].BytesDown
	})

	if len(list) > r.topN {
		list = list[:r.topN]
	}

	return Stats{
		Requests:     r.requests,
		BytesUp:      r.bytesUp,
		BytesDown:    r.bytesDown,
		Active:       r.active,
		DialFailures: r.dialFailures,
		Destinations: list,
	}
}

// bucket returns the DestStat for dest, folding into the overflow bucket once
// the tracked set is full. The caller holds r.mu.
func (r *Recorder) bucket(dest string) *DestStat {
	if stat, ok := r.dests[dest]; ok {
		return stat
	}

	if len(r.dests) >= maxTrackedDestinations {
		dest = overflowDestination
		if stat, ok := r.dests[dest]; ok {
			return stat
		}
	}

	stat := &DestStat{Destination: dest}
	r.dests[dest] = stat

	return stat
}
