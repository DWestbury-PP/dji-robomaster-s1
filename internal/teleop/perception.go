package teleop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Detection is one box from a fast tier, in pixels of the frame it came from.
type Detection struct {
	Label      string     `json:"label"`
	Confidence float64    `json:"confidence"`
	Box        [4]float64 `json:"box"` // x1, y1, x2, y2
}

// Observation is one tier's output for one frame.
//
// It carries the identity of the frame it was derived from because tiers run at
// wildly different cadences — a detector at 100 Hz and a scene model that takes
// nine seconds feed the same console. Without `FrameSeq` and `CapturedAt` there
// is no way to tell the operator that a narration describes a scene the robot
// has already driven away from, and it would be read as current.
type Observation struct {
	Tier       string      `json:"tier"`  // "fast", "scene", …
	Model      string      `json:"model"` // what produced it
	FrameSeq   uint64      `json:"frameSeq"`
	CapturedAt time.Time   `json:"capturedAt"`
	LatencyMs  float64     `json:"latencyMs"` // inference time on the producer
	Width      int         `json:"width"`     // source frame dimensions, for
	Height     int         `json:"height"`    // scaling boxes onto the canvas
	Detections []Detection `json:"detections,omitempty"`
	Text       string      `json:"text,omitempty"`
	Hazards    []string    `json:"hazards,omitempty"`
}

// Pending is a tier announcing that it has started work. A tier posts this when
// it begins a request and posts an Observation when it finishes, which is what
// lets the console distinguish "thinking" from "idle" — and therefore show a
// countdown that does not lie about what it knows.
type Pending struct {
	Tier  string `json:"tier"`
	Model string `json:"model"`
	// IntervalMs is the tier's cadence, so the console can count down to the
	// next capture rather than guessing.
	IntervalMs int64 `json:"intervalMs"`
}

// dated is an Observation plus what the console needs to render honestly.
type dated struct {
	Observation
	// AgeMs is how long ago the *frame* was captured — not how long ago the
	// result arrived. This is the number that stops stale narration reading as
	// current.
	AgeMs float64 `json:"ageMs"`
	// StaleMs is how long since this tier last reported at all, which tells you
	// whether the tier is alive as distinct from slow.
	StaleMs float64 `json:"staleMs"`

	// Thinking is true while a request is in flight.
	Thinking bool `json:"thinking"`
	// ElapsedMs is how long the in-flight request has been running. Only
	// meaningful while Thinking — we cannot know how much longer it will take,
	// so we report what has happened rather than predict what will.
	ElapsedMs float64 `json:"elapsedMs,omitempty"`
	// NextInMs counts down to the next capture. This one we do control, so it
	// is exact.
	NextInMs float64 `json:"nextInMs,omitempty"`
	// TypicalMs is a rolling median of recent completions — an expectation, not
	// a promise, and labelled as such in the console.
	TypicalMs float64 `json:"typicalMs,omitempty"`
	// IntervalMs is the tier's cadence, so a countdown can be drawn as a
	// proportion rather than a bare number.
	IntervalMs float64 `json:"intervalMs,omitempty"`
}

// perceptionStore keeps the newest observation per tier. Older results are
// replaced, never queued: a console wants the current belief, not a history.
// tierRun tracks the cadence of one tier so the console can show progress
// between observations rather than a field that only changes every 20 s.
type tierRun struct {
	thinking  bool
	startedAt time.Time
	nextAt    time.Time
	interval  time.Duration
	// recent completion durations, newest last, for a rolling typical.
	recent []time.Duration
}

// typical is the median of recent completions. Median rather than mean, so one
// cold start does not skew the expectation the operator is shown.
func (r *tierRun) typical() time.Duration {
	if len(r.recent) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), r.recent...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

type perceptionStore struct {
	mu     sync.RWMutex
	byTier map[string]Observation
	seenAt map[string]time.Time
	runs   map[string]*tierRun
}

func newPerceptionStore() *perceptionStore {
	return &perceptionStore{
		byTier: make(map[string]Observation),
		seenAt: make(map[string]time.Time),
		runs:   make(map[string]*tierRun),
	}
}

// run returns the tier's cadence record, creating it on first sight. Callers
// must hold the lock.
func (p *perceptionStore) run(tier string) *tierRun {
	r, ok := p.runs[tier]
	if !ok {
		r = &tierRun{}
		p.runs[tier] = r
	}
	return r
}

// pending records that a tier has started work.
func (p *perceptionStore) pending(q Pending) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.run(q.Tier)
	r.thinking = true
	r.startedAt = time.Now()
	if q.IntervalMs > 0 {
		r.interval = time.Duration(q.IntervalMs) * time.Millisecond
	}
}

func (p *perceptionStore) put(o Observation) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	p.byTier[o.Tier] = o
	p.seenAt[o.Tier] = now

	r := p.run(o.Tier)
	if r.thinking && !r.startedAt.IsZero() {
		// Keep a short window: a model that has warmed up should not be judged
		// forever by its first cold run.
		r.recent = append(r.recent, now.Sub(r.startedAt))
		if len(r.recent) > 8 {
			r.recent = r.recent[len(r.recent)-8:]
		}
	}
	r.thinking = false
	if r.interval > 0 {
		r.nextAt = now.Add(r.interval)
	}
}

func (p *perceptionStore) snapshot() map[string]dated {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[string]dated, len(p.byTier))
	now := time.Now()
	ms := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

	// A tier that has announced itself but not yet answered still deserves to
	// appear, so the console can show it working from the very first cycle.
	tiers := make(map[string]struct{}, len(p.byTier)+len(p.runs))
	for t := range p.byTier {
		tiers[t] = struct{}{}
	}
	for t := range p.runs {
		tiers[t] = struct{}{}
	}

	for tier := range tiers {
		d := dated{Observation: p.byTier[tier]}
		d.Tier = tier

		if o, ok := p.byTier[tier]; ok && !o.CapturedAt.IsZero() {
			d.AgeMs = ms(now.Sub(o.CapturedAt))
		}
		if seen, ok := p.seenAt[tier]; ok {
			d.StaleMs = ms(now.Sub(seen))
		}
		if r, ok := p.runs[tier]; ok {
			d.TypicalMs = ms(r.typical())
			d.IntervalMs = ms(r.interval)
			if r.thinking {
				d.Thinking = true
				d.ElapsedMs = ms(now.Sub(r.startedAt))
			} else if !r.nextAt.IsZero() {
				if left := r.nextAt.Sub(now); left > 0 {
					d.NextInMs = ms(left)
				}
			}
		}
		out[tier] = d
	}
	return out
}

// handlePending accepts a tier's "I have started" marker.
func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	var q Pending
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&q); err != nil {
		http.Error(w, "bad pending: "+err.Error(), http.StatusBadRequest)
		return
	}
	if q.Tier == "" {
		http.Error(w, "pending needs a tier", http.StatusBadRequest)
		return
	}
	s.perception.pending(q)
	w.WriteHeader(http.StatusNoContent)
}

// handleFrame serves the newest frame to a consumer that pulls on its own
// schedule. See FrameHub.Newest for why slow tiers must pull rather than be
// pushed to.
//
// The headers carry the frame's identity so a consumer can attribute its result
// without parsing the image.
func (s *Server) handleFrame(w http.ResponseWriter, r *http.Request) {
	f, ok := s.hub.Newest()
	if !ok {
		http.Error(w, "no frame yet", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Seq", fmt.Sprint(f.Seq))
	w.Header().Set("X-Frame-Captured-Unix-Ms", fmt.Sprint(f.CapturedAt.UnixMilli()))
	w.Header().Set("X-Frame-Age-Ms", fmt.Sprintf("%.1f", float64(f.Age().Nanoseconds())/1e6))
	w.Header().Set("X-Frame-Width", fmt.Sprint(f.Width))
	w.Header().Set("X-Frame-Height", fmt.Sprint(f.Height))
	w.Header().Set("Content-Length", fmt.Sprint(len(f.JPEG)))
	_, _ = w.Write(f.JPEG)
}

// handlePerception accepts one tier's result. Producers post whenever they have
// something; nothing here blocks them, and a slow tier cannot delay a fast one.
func (s *Server) handlePerception(w http.ResponseWriter, r *http.Request) {
	var o Observation
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&o); err != nil {
		http.Error(w, "bad observation: "+err.Error(), http.StatusBadRequest)
		return
	}
	if o.Tier == "" {
		http.Error(w, "observation needs a tier", http.StatusBadRequest)
		return
	}
	s.perception.put(o)
	if s.rec != nil {
		s.rec.Observation(o.Tier, o.Model, o.Text, o.FrameSeq, o.LatencyMs, len(o.Detections))
	}
	w.WriteHeader(http.StatusNoContent)
}
