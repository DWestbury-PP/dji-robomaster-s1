// Package fleet lets one console drive several vehicles.
//
// The DJI bridge is a process-wide singleton (DECISIONS.md #9), so a second
// vehicle cannot be a second client in the same process — it has to be a second
// process. This package is the supervisor's side of that: a handle to one
// worker, talking the same WebSocket protocol the browser already speaks.
//
// Nothing here decides safety. Each worker keeps its own governor on the last
// hop before its own wire, exactly where #6 put it, and a supervisor that
// stopped relaying is indistinguishable to that governor from a browser that
// closed — the deadman stops the vehicle within 250 ms either way. That is why
// switching vehicles needs no explicit stop: the machinery already does it.
package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Identity is who a worker says it is driving. Reported by the worker rather
// than configured here: the MAC comes from the robot's own broadcast, so two
// vehicles are distinguishable with no pairing or QR setup.
type Identity struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AppID     uint64 `json:"appID,omitempty"`
	MAC       string `json:"mac,omitempty"`
	Connected bool   `json:"connected"`
}

// Telemetry is the worker's HUD payload, relayed to the browser untouched.
type Telemetry map[string]any

// Worker is a handle to one vehicle process.
type Worker struct {
	Addr string

	log *slog.Logger

	mu    sync.RWMutex
	ident Identity
	last  Telemetry
	seen  time.Time

	conn atomic.Pointer[websocket.Conn]
	up   atomic.Bool
}

// NewWorker creates a handle to one vehicle process. The name is what the
// console shows before the worker answers for itself — which matters most when
// it never does: a vehicle whose robot is switched off should still appear in
// the dropdown as "Bravo (down)" rather than as a bare address.
func NewWorker(addr, name string, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	if name == "" {
		name = addr
	}
	return &Worker{
		Addr:  addr,
		log:   log.With("worker", addr),
		ident: Identity{ID: name, Name: name},
	}
}

// Up reports whether the supervisor currently holds a live socket to this
// worker. A worker that is down is not a vehicle you can drive, and the console
// must say so rather than accept commands into a void.
func (w *Worker) Up() bool { return w.up.Load() }

func (w *Worker) Identity() Identity {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ident
}

func (w *Worker) Telemetry() (Telemetry, time.Time) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.last, w.seen
}

// Run keeps a socket to the worker, reconnecting for as long as ctx lives. It
// never returns an error: a worker being down is a normal state the console
// shows, not a failure that should take the supervisor with it.
func (w *Worker) Run(ctx context.Context) {
	backoff := 250 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for ctx.Err() == nil {
		if err := w.session(ctx); err != nil && ctx.Err() == nil {
			w.log.Debug("worker link down", "err", err)
		}
		w.up.Store(false)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (w *Worker) session(ctx context.Context) error {
	w.refreshIdentity(ctx)

	c, _, err := websocket.Dial(ctx, "ws://"+w.Addr+"/ws", nil)
	if err != nil {
		return err
	}
	defer c.CloseNow()

	w.conn.Store(c)
	w.up.Store(true)
	defer func() {
		w.conn.Store(nil)
		w.up.Store(false)
	}()

	w.log.Info("worker connected")

	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		var t Telemetry
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		w.mu.Lock()
		w.last, w.seen = t, time.Now()
		if conn, ok := t["connected"].(bool); ok {
			w.ident.Connected = conn
		}
		w.mu.Unlock()
	}
}

// refreshIdentity asks the worker which vehicle it holds. Best effort: a worker
// that cannot answer still gets driven, it just shows under its address.
func (w *Worker) refreshIdentity(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+w.Addr+"/vehicle", nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var id Identity
	if err := json.NewDecoder(resp.Body).Decode(&id); err != nil {
		return
	}
	if id.ID == "" {
		id.ID = w.Addr
	}
	if id.Name == "" {
		id.Name = id.ID
	}
	w.mu.Lock()
	w.ident = id
	w.mu.Unlock()
}

// Send relays one console message to this worker verbatim. The supervisor does
// not interpret commands and must not: every limit belongs to the governor in
// the worker, on the last hop before the wire.
func (w *Worker) Send(ctx context.Context, raw []byte) error {
	c := w.conn.Load()
	if c == nil {
		return fmt.Errorf("worker %s is not connected", w.Addr)
	}
	return c.Write(ctx, websocket.MessageText, raw)
}
