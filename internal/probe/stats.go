// Package probe holds the measurement primitives for the M1 latency harness.
package probe

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"time"
)

// Series accumulates durations and reports the distribution. The interesting
// number for a control link is never the mean — it is the tail, because a
// deadman timer fires on the worst case, not the average (ARCHITECTURE.md §5).
type Series struct {
	Name    string
	samples []time.Duration
}

func NewSeries(name string) *Series { return &Series{Name: name} }

func (s *Series) Add(d time.Duration) { s.samples = append(s.samples, d) }

func (s *Series) Len() int { return len(s.samples) }

// Summary is the JSON-serializable form of a Series. Durations are reported in
// milliseconds because that is the unit the latency budget is written in.
type Summary struct {
	Name   string  `json:"name"`
	N      int     `json:"n"`
	MinMs  float64 `json:"min_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	P99Ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`
	MeanMs float64 `json:"mean_ms"`
	// StdDevMs is the jitter figure. For the video series this is the number
	// that decides whether the link is usable under motion.
	StdDevMs float64 `json:"stddev_ms"`
}

func (s *Series) Summary() Summary {
	out := Summary{Name: s.Name, N: len(s.samples)}
	if len(s.samples) == 0 {
		return out
	}

	sorted := make([]time.Duration, len(s.samples))
	copy(sorted, s.samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	ms := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

	var sum float64
	for _, d := range sorted {
		sum += ms(d)
	}
	out.MeanMs = sum / float64(len(sorted))

	var sq float64
	for _, d := range sorted {
		delta := ms(d) - out.MeanMs
		sq += delta * delta
	}
	out.StdDevMs = math.Sqrt(sq / float64(len(sorted)))

	out.MinMs = ms(sorted[0])
	out.MaxMs = ms(sorted[len(sorted)-1])
	out.P50Ms = ms(percentile(sorted, 0.50))
	out.P95Ms = ms(percentile(sorted, 0.95))
	out.P99Ms = ms(percentile(sorted, 0.99))

	return out
}

// percentile uses nearest-rank; with the sample counts this harness collects,
// interpolation would imply a precision we do not have.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Report is the whole M1 result, written as JSON so runs can be diffed across
// Wi-Fi modes, distances and firmware states.
type Report struct {
	StartedAt  time.Time `json:"started_at"`
	Host       Host      `json:"host"`
	WiFiMode   string    `json:"wifi_mode"`
	ConnectMs  float64   `json:"connect_ms"`
	FirstFrame float64   `json:"first_frame_ms"`
	Video      Video     `json:"video"`
	Series     []Summary `json:"series"`
	CPU        CPU       `json:"cpu"`
	Notes      []string  `json:"notes,omitempty"`
	Incomplete bool      `json:"incomplete"`
}

type Host struct {
	GOARCH       string `json:"goarch"`
	GOOS         string `json:"goos"`
	UnderRosetta bool   `json:"under_rosetta"`
	GoVersion    string `json:"go_version"`
}

type Video struct {
	Frames      int     `json:"frames"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	DurationSec float64 `json:"duration_sec"`
	FPS         float64 `json:"fps"`
	// FPSDeficit is measured FPS against the S1's nominal 30. A large deficit
	// with pinned CPU is the signature of decode losing to emulation.
	FPSDeficit float64 `json:"fps_deficit_pct"`
}

type CPU struct {
	UserSec   float64 `json:"user_sec"`
	SystemSec float64 `json:"system_sec"`
	WallSec   float64 `json:"wall_sec"`
	// CoresBusy is (user+system)/wall — how many cores the process burned.
	CoresBusy float64 `json:"cores_busy"`
}

func (r *Report) WriteJSON(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
