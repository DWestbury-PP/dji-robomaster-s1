package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// Fleet is the set of vehicles this console can drive.
type Fleet struct {
	log *slog.Logger

	mu      sync.RWMutex
	workers []*Worker
	byID    map[string]*Worker

	// estopped latches fleet-wide. It is held here, and not only in each
	// worker's governor, for one reason: a worker that drops and reconnects
	// during an e-stop would otherwise come back armed-and-willing. Re-asserting
	// on every connect is what makes "stopped" survive a reconnect.
	estopped atomic.Bool
}

// New builds a fleet from entries of the form "addr" or "Name=addr". The name
// form lets the console label a vehicle that never comes up.
func New(addrs []string, log *slog.Logger) *Fleet {
	if log == nil {
		log = slog.Default()
	}
	f := &Fleet{log: log, byID: make(map[string]*Worker, len(addrs))}
	for _, entry := range addrs {
		name, addr := "", entry
		if i := strings.Index(entry, "="); i > 0 {
			name, addr = entry[:i], entry[i+1:]
		}
		w := NewWorker(addr, name, log)
		f.workers = append(f.workers, w)
		f.byID[addr] = w
		if name != "" {
			f.byID[name] = w
		}
	}
	return f
}

// Run supervises every worker link until ctx ends.
func (f *Fleet) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, w := range f.Workers() {
		wg.Add(1)
		go func(w *Worker) {
			defer wg.Done()
			f.runWorker(ctx, w)
		}(w)
	}
	wg.Wait()
}

// runWorker wraps Worker.Run so that every successful (re)connect re-asserts a
// latched e-stop before the console can route a single command to it.
func (f *Fleet) runWorker(ctx context.Context, w *Worker) {
	go func() {
		was := false
		for ctx.Err() == nil {
			now := w.Up()
			if now && !was && f.estopped.Load() {
				if err := w.Send(ctx, estopMsg); err != nil {
					f.log.Warn("could not re-assert e-stop on reconnect",
						"worker", w.Addr, "err", err)
				} else {
					f.log.Warn("re-asserted latched e-stop on reconnect", "worker", w.Addr)
				}
			}
			was = now
			select {
			case <-ctx.Done():
				return
			case <-tick():
			}
		}
	}()
	w.Run(ctx)
}

func (f *Fleet) Workers() []*Worker {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*Worker, len(f.workers))
	copy(out, f.workers)
	return out
}

// Get resolves a vehicle by worker address or by the ID the worker reports.
func (f *Fleet) Get(id string) (*Worker, bool) {
	f.mu.RLock()
	w, ok := f.byID[id]
	f.mu.RUnlock()
	if ok {
		return w, true
	}
	for _, w := range f.Workers() {
		if w.Identity().ID == id {
			return w, true
		}
	}
	return nil, false
}

// First is the vehicle a new console session starts on: the first one that is
// actually up, so a fresh tab lands on something drivable rather than on a
// worker whose robot is switched off.
func (f *Fleet) First() (*Worker, bool) {
	ws := f.Workers()
	for _, w := range ws {
		if w.Up() {
			return w, true
		}
	}
	if len(ws) > 0 {
		return ws[0], true
	}
	return nil, false
}

var (
	estopMsg = mustJSON(map[string]any{"type": "estop"})
	clearMsg = mustJSON(map[string]any{"type": "clear_estop"})
)

// EStopAll halts every vehicle, not just the one the operator had selected.
// Deliberate: in a multi-vehicle session the person who sees the collision
// coming is often not the one driving that vehicle, and a safety control that
// only reaches your own robot is not a safety control.
func (f *Fleet) EStopAll(ctx context.Context) error {
	f.estopped.Store(true)
	return f.broadcast(ctx, estopMsg)
}

// ClearEStopAll releases the fleet-wide stop. Vehicles stay stopped until a
// human commands them again — clearing does not resume motion, because the
// governor refuses commands submitted during a stop rather than latching them.
func (f *Fleet) ClearEStopAll(ctx context.Context) error {
	f.estopped.Store(false)
	return f.broadcast(ctx, clearMsg)
}

func (f *Fleet) EStopped() bool { return f.estopped.Load() }

// broadcast sends to every worker and reports every failure. It does not stop
// at the first error: an e-stop that reached two of three vehicles must say so,
// and must still have reached the two.
func (f *Fleet) broadcast(ctx context.Context, raw []byte) error {
	var errs []error
	for _, w := range f.Workers() {
		if !w.Up() {
			// A worker with no live socket is receiving no commands either, so
			// its own deadman has already stopped it. Not an error.
			continue
		}
		if err := w.Send(ctx, raw); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
