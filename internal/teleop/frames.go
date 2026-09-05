// Package teleop serves the browser control console: MJPEG video, a WebSocket
// carrying commands up and telemetry down, and the static UI.
//
// It runs inside the driver process rather than as a separate Python service
// over the bus (DECISIONS.md #13). The frames are already here, and JPEG encode
// measured 23.6 ms per frame — too expensive to pay twice, and far too
// expensive to pay once per connected browser.
package teleop

import (
	"bytes"
	"image"
	"image/jpeg"
	"sync"
	"time"
)

// FrameHub takes raw RGB frames from the camera at whatever rate they arrive,
// encodes at most StreamFPS of them, and fans the result out to every viewer.
//
// The encode happens exactly once per published frame no matter how many
// browsers are watching. Frames that arrive between encodes are dropped, not
// queued: a teleop view wants the newest frame, never a backlog.
type FrameHub struct {
	quality int

	mu         sync.Mutex
	raw        []byte // newest raw RGB24, owned by us
	rawW       int
	rawH       int
	rawSeq     uint64
	rawAt      time.Time
	encSeq     uint64
	jpeg       []byte
	encAt      time.Time
	capturedAt time.Time // when the frame in `jpeg` was captured, not encoded
	encW, encH int
	updated    chan struct{} // closed and replaced on each new JPEG
}

// Frame is an encoded frame with the identity a consumer needs to date its own
// output. Perception results carry these back so the console can show how old
// an observation is — essential when one tier runs at 100 Hz and another takes
// nine seconds (docs/M4.md).
type Frame struct {
	JPEG []byte
	// Seq is the capture sequence number; it identifies this exact frame.
	Seq uint64
	// CapturedAt is when the camera produced it, NOT when we encoded it.
	CapturedAt time.Time
	Width      int
	Height     int
}

// Age is how long ago this frame was captured.
func (f Frame) Age() time.Duration {
	if f.CapturedAt.IsZero() {
		return 0
	}
	return time.Since(f.CapturedAt)
}

func NewFrameHub(quality int) *FrameHub {
	if quality <= 0 || quality > 100 {
		quality = 70
	}
	return &FrameHub{quality: quality, updated: make(chan struct{})}
}

// Submit publishes a raw RGB24 frame. The caller's buffer is copied, because
// the camera library reuses it. Cheap relative to the encode we are avoiding.
func (h *FrameHub) Submit(pix []byte, w, height int) {
	if w <= 0 || height <= 0 || len(pix) < w*height*3 {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if cap(h.raw) < len(pix) {
		h.raw = make([]byte, len(pix))
	}
	h.raw = h.raw[:len(pix)]
	copy(h.raw, pix)
	h.rawW, h.rawH = w, height
	h.rawSeq++
	h.rawAt = time.Now()
}

// Run encodes the newest raw frame at fps until ctx-like stop is closed.
func (h *FrameHub) Run(stop <-chan struct{}, fps int) {
	if fps <= 0 {
		fps = 15
	}
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()

	var buf bytes.Buffer
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			h.mu.Lock()
			if h.rawSeq == h.encSeq || h.rawW == 0 {
				h.mu.Unlock()
				continue // nothing new since the last encode
			}
			seq := h.rawSeq
			capturedAt := h.rawAt
			w, ht := h.rawW, h.rawH
			src := make([]byte, len(h.raw))
			copy(src, h.raw)
			h.mu.Unlock()

			buf.Reset()
			if err := jpeg.Encode(&buf, rgbToRGBA(src, w, ht), &jpeg.Options{Quality: h.quality}); err != nil {
				continue
			}
			out := make([]byte, buf.Len())
			copy(out, buf.Bytes())

			h.mu.Lock()
			h.jpeg = out
			h.encSeq = seq
			h.encAt = time.Now()
			h.capturedAt = capturedAt
			h.encW, h.encH = w, ht
			close(h.updated)
			h.updated = make(chan struct{})
			h.mu.Unlock()
		}
	}
}

// Latest returns the newest encoded frame and a channel that closes when a
// newer one is ready. Viewers block on the channel rather than polling.
func (h *FrameHub) Latest() ([]byte, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.jpeg, h.updated
}

// Newest returns the current frame with its identity, for consumers that pull
// on their own schedule rather than being pushed to.
//
// This is the access pattern for slow tiers. A pushed stream would queue stale
// frames in the socket while a slow consumer thinks, so by the time it read one
// it would be reasoning about a scene that had passed. Pulling makes that
// impossible: you ask when you are ready, and you get what is true now.
func (h *FrameHub) Newest() (Frame, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.jpeg) == 0 {
		return Frame{}, false
	}
	return Frame{
		JPEG:       h.jpeg,
		Seq:        h.encSeq,
		CapturedAt: h.capturedAt,
		Width:      h.encW,
		Height:     h.encH,
	}, true
}

// AgeMs is how long ago the newest frame was encoded — the HUD's honest answer
// to "is this picture current?".
func (h *FrameHub) AgeMs() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.encAt.IsZero() {
		return -1
	}
	return float64(time.Since(h.encAt).Nanoseconds()) / 1e6
}

// rgbToRGBA widens packed RGB24 to RGBA without the per-pixel At() path, which
// would dominate the encode.
func rgbToRGBA(src []byte, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		si := y * w * 3
		di := y * dst.Stride
		for x := 0; x < w; x++ {
			dst.Pix[di+0] = src[si+0]
			dst.Pix[di+1] = src[si+1]
			dst.Pix[di+2] = src[si+2]
			dst.Pix[di+3] = 255
			si += 3
			di += 4
		}
	}
	return dst
}
