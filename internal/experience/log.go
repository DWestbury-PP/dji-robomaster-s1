// Package experience records a drive: what the robot saw, what the models said
// about it, and — the part that cannot be recovered later — what the operator
// did while looking at each frame.
//
// Frames and captions can be regenerated from a corpus at any time by re-running
// a model. The human's control inputs exist only if they are captured at the
// moment they happen, which is why this is the one part of the system with a
// deadline (DECISIONS.md #15).
//
// The recorder must never be able to affect driving. Every write goes through a
// bounded channel to a single writer goroutine; when that channel is full the
// event is dropped and counted rather than blocking the caller. A lost log line
// is a nuisance. A stalled control loop is a runaway robot.
package experience

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Event is one line of the log. Everything is timestamped in milliseconds since
// the drive began, so a whole drive can be replayed on a single timeline
// without reconciling clocks.
type Event struct {
	Ms   int64  `json:"ms"`
	Type string `json:"type"`

	// frame
	Seq        uint64 `json:"seq,omitempty"`
	File       string `json:"file,omitempty"`
	CapturedMs int64  `json:"capturedMs,omitempty"`

	// control — what was asked for, and what the governor actually allowed
	Source  string      `json:"source,omitempty"`
	Req     *[4]float64 `json:"req,omitempty"` // chassisX, chassisY, gimbalX, gimbalY
	Applied *[4]float64 `json:"applied,omitempty"`
	Reason  string      `json:"reason,omitempty"`
	Fire    int         `json:"fire,omitempty"`

	// observation
	Tier      string  `json:"tier,omitempty"`
	Model     string  `json:"model,omitempty"`
	Text      string  `json:"text,omitempty"`
	LatencyMs float64 `json:"latencyMs,omitempty"`
	FrameSeq  uint64  `json:"frameSeq,omitempty"`
	Objects   int     `json:"objects,omitempty"`

	// vehicle
	Connected *bool `json:"connected,omitempty"`
	Battery   int   `json:"battery,omitempty"`

	// note
	Note string `json:"note,omitempty"`
}

// Recorder writes one drive.
type Recorder struct {
	dir     string
	started time.Time

	events chan Event
	frames chan framePayload
	done   chan struct{}
	wg     sync.WaitGroup

	dropped atomic.Uint64
	written atomic.Uint64
	nFrames atomic.Uint64

	closeOnce sync.Once
}

type framePayload struct {
	ev   Event
	data []byte
}

// Manifest is written at the end so a drive can be found and understood without
// parsing the whole log.
type Manifest struct {
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	DurationS float64   `json:"durationSec"`
	Events    uint64    `json:"events"`
	Frames    uint64    `json:"frames"`
	Dropped   uint64    `json:"dropped"`
	Note      string    `json:"note,omitempty"`
}

// New starts a recorder writing into root/<timestamp>/.
func New(root, note string) (*Recorder, error) {
	started := time.Now()
	dir := filepath.Join(root, started.Format("20060102-150405"))
	if err := os.MkdirAll(filepath.Join(dir, "frames"), 0o755); err != nil {
		return nil, err
	}

	r := &Recorder{
		dir:     dir,
		started: started,
		// Generous buffers: a burst of control events during a fast manoeuvre
		// should not start dropping just because the disk hiccupped.
		events: make(chan Event, 4096),
		frames: make(chan framePayload, 64),
		done:   make(chan struct{}),
	}

	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}

	r.wg.Add(1)
	go r.writeLoop(f, note)
	return r, nil
}

func (r *Recorder) Dir() string { return r.dir }

// Stats reports what has been recorded and what was lost, so the console can
// show honestly that a drive is being captured.
func (r *Recorder) Stats() (events, frames, dropped uint64) {
	return r.written.Load(), r.nFrames.Load(), r.dropped.Load()
}

func (r *Recorder) writeLoop(f *os.File, note string) {
	defer r.wg.Done()
	defer f.Close()

	enc := json.NewEncoder(f)
	for {
		select {
		case <-r.done:
			// Drain whatever is already queued so the tail of a drive is not
			// lost to a shutdown.
			for {
				select {
				case e := <-r.events:
					_ = enc.Encode(e)
					r.written.Add(1)
				case p := <-r.frames:
					r.writeFrame(enc, p)
				default:
					r.writeManifest(note)
					return
				}
			}
		case e := <-r.events:
			_ = enc.Encode(e)
			r.written.Add(1)
		case p := <-r.frames:
			r.writeFrame(enc, p)
		}
	}
}

func (r *Recorder) writeFrame(enc *json.Encoder, p framePayload) {
	n := r.nFrames.Add(1)
	name := fmt.Sprintf("frames/%06d.jpg", n-1)
	if err := os.WriteFile(filepath.Join(r.dir, name), p.data, 0o644); err != nil {
		return
	}
	p.ev.File = name
	_ = enc.Encode(p.ev)
	r.written.Add(1)
}

func (r *Recorder) writeManifest(note string) {
	end := time.Now()
	m := Manifest{
		StartedAt: r.started, EndedAt: end,
		DurationS: end.Sub(r.started).Seconds(),
		Events:    r.written.Load(), Frames: r.nFrames.Load(),
		Dropped: r.dropped.Load(), Note: note,
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(r.dir, "manifest.json"), append(b, '\n'), 0o644)
}

// emit queues an event, dropping it rather than blocking. See the package note:
// the recorder is never allowed to be the reason the robot did not stop.
func (r *Recorder) emit(e Event) {
	e.Ms = time.Since(r.started).Milliseconds()
	select {
	case r.events <- e:
	default:
		r.dropped.Add(1)
	}
}

// Control records a control tick: what was requested and what was allowed.
// Callers should only call this when something changed, plus a low-rate
// heartbeat — the signal is a step function, so change-plus-heartbeat replays
// exactly and costs a fraction of the disk.
func (r *Recorder) Control(source string, req, applied [4]float64, reason string, fire int) {
	rq, ap := req, applied
	r.emit(Event{Type: "control", Source: source, Req: &rq, Applied: &ap, Reason: reason, Fire: fire})
}

// Frame records an image and its identity.
func (r *Recorder) Frame(seq uint64, capturedAt time.Time, jpeg []byte) {
	data := make([]byte, len(jpeg))
	copy(data, jpeg)
	p := framePayload{
		ev: Event{
			Ms: time.Since(r.started).Milliseconds(), Type: "frame",
			Seq: seq, CapturedMs: capturedAt.UnixMilli(),
		},
		data: data,
	}
	select {
	case r.frames <- p:
	default:
		r.dropped.Add(1)
	}
}

// Observation records what a tier said about a frame.
func (r *Recorder) Observation(tier, model, text string, frameSeq uint64, latencyMs float64, objects int) {
	r.emit(Event{
		Type: "observation", Tier: tier, Model: model, Text: text,
		FrameSeq: frameSeq, LatencyMs: latencyMs, Objects: objects,
	})
}

// Vehicle records link and battery changes.
func (r *Recorder) Vehicle(connected bool, battery int) {
	c := connected
	r.emit(Event{Type: "vehicle", Connected: &c, Battery: battery})
}

// Note records a free-text marker — useful for "this is where it got stuck".
func (r *Recorder) Note(text string) { r.emit(Event{Type: "note", Note: text}) }

// Close flushes and writes the manifest. Safe to call more than once.
func (r *Recorder) Close() {
	r.closeOnce.Do(func() {
		close(r.done)
		r.wg.Wait()
	})
}
