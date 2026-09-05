package teleop

import (
	"encoding/json"
	"fmt"
	"net/http"
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
}

// perceptionStore keeps the newest observation per tier. Older results are
// replaced, never queued: a console wants the current belief, not a history.
type perceptionStore struct {
	mu     sync.RWMutex
	byTier map[string]Observation
	seenAt map[string]time.Time
}

func newPerceptionStore() *perceptionStore {
	return &perceptionStore{
		byTier: make(map[string]Observation),
		seenAt: make(map[string]time.Time),
	}
}

func (p *perceptionStore) put(o Observation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byTier[o.Tier] = o
	p.seenAt[o.Tier] = time.Now()
}

func (p *perceptionStore) snapshot() map[string]dated {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[string]dated, len(p.byTier))
	now := time.Now()
	for tier, o := range p.byTier {
		d := dated{Observation: o}
		if !o.CapturedAt.IsZero() {
			d.AgeMs = float64(now.Sub(o.CapturedAt).Nanoseconds()) / 1e6
		}
		if seen, ok := p.seenAt[tier]; ok {
			d.StaleMs = float64(now.Sub(seen).Nanoseconds()) / 1e6
		}
		out[tier] = d
	}
	return out
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
	w.WriteHeader(http.StatusNoContent)
}
