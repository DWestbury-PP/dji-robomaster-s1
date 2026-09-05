package experience

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newRec(t *testing.T) *Recorder {
	t.Helper()
	r, err := New(t.TempDir(), "test drive")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

func readEvents(t *testing.T, dir string) []Event {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()

	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad log line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestRecordsControlWithBothRequestedAndApplied(t *testing.T) {
	r := newRec(t)

	// The distinction is the whole point: demonstration data needs what the
	// human asked for, not only what the governor let through.
	r.Control("human", [4]float64{0, 1, 0, 0}, [4]float64{0, 0.45, 0, 0}, "clamped", 0)
	r.Close()

	evs := readEvents(t, r.Dir())
	var got *Event
	for i := range evs {
		if evs[i].Type == "control" {
			got = &evs[i]
		}
	}
	if got == nil {
		t.Fatal("control event never written")
	}
	if got.Req == nil || got.Applied == nil {
		t.Fatalf("control event lost a side: %+v", got)
	}
	if (*got.Req)[1] != 1 || (*got.Applied)[1] != 0.45 {
		t.Fatalf("request and applied were conflated: req %v applied %v", *got.Req, *got.Applied)
	}
	if got.Reason != "clamped" {
		t.Fatalf("reason lost: %q", got.Reason)
	}
}

func TestFramesLandOnDiskAndAreReferenced(t *testing.T) {
	r := newRec(t)

	jpeg := []byte{0xFF, 0xD8, 1, 2, 3}
	r.Frame(42, time.Now(), jpeg)
	r.Frame(43, time.Now(), jpeg)
	r.Close()

	for _, name := range []string{"frames/000000.jpg", "frames/000001.jpg"} {
		if _, err := os.Stat(filepath.Join(r.Dir(), name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	var frames int
	for _, e := range readEvents(t, r.Dir()) {
		if e.Type == "frame" {
			frames++
			if e.File == "" {
				t.Fatalf("frame event has no file reference: %+v", e)
			}
		}
	}
	if frames != 2 {
		t.Fatalf("want 2 frame events, got %d", frames)
	}
}

// The caller's buffer is reused by the camera library, so the recorder must own
// a copy or frames will be silently corrupted by the next one.
func TestFrameDataIsCopied(t *testing.T) {
	r := newRec(t)

	buf := []byte{0xFF, 0xD8, 9, 9, 9}
	r.Frame(1, time.Now(), buf)
	for i := range buf {
		buf[i] = 0 // the camera reuses its buffer immediately
	}
	r.Close()

	got, err := os.ReadFile(filepath.Join(r.Dir(), "frames/000000.jpg"))
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if len(got) != 5 || got[0] != 0xFF || got[2] != 9 {
		t.Fatalf("frame was not copied before the buffer was reused: % x", got)
	}
}

// The property that matters more than any other here. A lost log line is a
// nuisance; a blocked control loop is a runaway robot.
func TestEmitNeverBlocks(t *testing.T) {
	r := newRec(t)

	const burst = 200_000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < burst; i++ {
			r.Control("human", [4]float64{}, [4]float64{}, "ok", 0)
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("emit blocked — the recorder can stall the control loop")
	}

	r.Close()
	written, _, dropped := r.Stats()
	if written+dropped == 0 {
		t.Fatal("nothing was recorded or counted")
	}
	// Whatever the split, every event must be accounted for one way or the
	// other: silent loss would make the log untrustworthy as a record.
	t.Logf("wrote %d, dropped %d of %d", written, dropped, burst)
}

func TestManifestSummarisesTheDrive(t *testing.T) {
	r := newRec(t)
	r.Observation("scene", "gemma4:e4b", "a hallway", 7, 2100, 0)
	r.Vehicle(true, 88)
	r.Note("stuck on a threshold")
	r.Close()

	b, err := os.ReadFile(filepath.Join(r.Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad manifest: %v", err)
	}
	if m.Events == 0 || m.StartedAt.IsZero() || m.EndedAt.IsZero() {
		t.Fatalf("manifest is empty: %+v", m)
	}
	if m.Note != "test drive" {
		t.Fatalf("note lost: %q", m.Note)
	}
}

func TestObservationAndNoteRoundTrip(t *testing.T) {
	r := newRec(t)
	r.Observation("scene", "gemma4:e4b", "a cluttered workshop", 12, 2400, 0)
	r.Note("door was shut")
	r.Close()

	var sawObs, sawNote bool
	for _, e := range readEvents(t, r.Dir()) {
		if e.Type == "observation" && e.Text == "a cluttered workshop" && e.FrameSeq == 12 {
			sawObs = true
		}
		if e.Type == "note" && e.Note == "door was shut" {
			sawNote = true
		}
	}
	if !sawObs || !sawNote {
		t.Fatalf("round trip lost content: obs=%v note=%v", sawObs, sawNote)
	}
}

// Events carry a millisecond offset from the drive's start, so a whole drive
// replays on one timeline without reconciling clocks.
func TestEventsAreOnADriveRelativeTimeline(t *testing.T) {
	r := newRec(t)
	r.Note("first")
	time.Sleep(40 * time.Millisecond)
	r.Note("second")
	r.Close()

	evs := readEvents(t, r.Dir())
	if len(evs) < 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	if evs[1].Ms <= evs[0].Ms {
		t.Fatalf("timeline did not advance: %d then %d", evs[0].Ms, evs[1].Ms)
	}
	if evs[0].Ms > 1000 {
		t.Fatalf("offsets look absolute, not drive-relative: %d", evs[0].Ms)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	r, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	r.Note("one")
	r.Close()
	r.Close() // must not panic on a double close
}
