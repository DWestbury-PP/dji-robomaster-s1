package teleop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/fleet"
)

// Supervisor is the console when more than one vehicle is in play. It holds no
// bridge, no governor and no control loop — those live in the worker process
// that owns each robot, which is what keeps safety on the last hop (#6, #9).
//
// It relays console messages to the selected worker **verbatim**. The one
// exception is e-stop, which it fans out to every vehicle; intercepting there
// widens a safety control and never narrows one.
type Supervisor struct {
	fleet *fleet.Fleet
	log   *slog.Logger
}

func NewSupervisor(f *fleet.Fleet, log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{fleet: f, log: log}
}

func (s *Supervisor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleUI)
	mux.HandleFunc("GET /vehicles", s.handleVehicles)
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("GET /stream", s.proxy("/stream"))
	mux.HandleFunc("GET /frame.jpg", s.proxy("/frame.jpg"))
	mux.HandleFunc("POST /perception", s.proxy("/perception"))
	mux.HandleFunc("POST /perception/pending", s.proxy("/perception/pending"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}

func (s *Supervisor) handleUI(w http.ResponseWriter, r *http.Request) {
	b, err := uiFS.ReadFile("ui.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

type vehicleView struct {
	fleet.Identity
	Up bool `json:"up"`
}

func (s *Supervisor) vehicles() []vehicleView {
	ws := s.fleet.Workers()
	out := make([]vehicleView, 0, len(ws))
	for _, w := range ws {
		out = append(out, vehicleView{Identity: w.Identity(), Up: w.Up()})
	}
	return out
}

func (s *Supervisor) handleVehicles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"vehicles": s.vehicles(),
		"estopped": s.fleet.EStopped(),
	})
}

// resolve picks the worker a request is aimed at: ?vehicle=<id>, else the first
// one that is up. Perception tiers and the video tag both use this, so a tier
// pointed at the supervisor with no vehicle still works for a single robot.
func (s *Supervisor) resolve(r *http.Request) (*fleet.Worker, bool) {
	if id := r.URL.Query().Get("vehicle"); id != "" {
		return s.fleet.Get(id)
	}
	return s.fleet.First()
}

// proxy forwards a request to the selected worker. Video is a long-lived
// multipart stream, so the response is copied rather than buffered, and the
// upstream request carries the client's context so closing the tab closes the
// upstream read too.
func (s *Supervisor) proxy(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worker, ok := s.resolve(r)
		if !ok {
			http.Error(w, "no such vehicle", http.StatusNotFound)
			return
		}
		if !worker.Up() {
			http.Error(w, "vehicle is not connected", http.StatusServiceUnavailable)
			return
		}

		url := "http://" + worker.Addr + path
		req, err := http.NewRequestWithContext(r.Context(), r.Method, url, r.Body)
		if err != nil {
			http.Error(w, "bad upstream request", http.StatusInternalServerError)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			req.Header.Set("Content-Type", ct)
		}

		// No client timeout: /stream never completes by design.
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			http.Error(w, "vehicle unreachable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)

		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 32*1024)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}
}

// session is one browser tab. Selection is per-session: two tabs can drive two
// different vehicles at once, which is the whole point of the dropdown.
type session struct {
	mu       sync.RWMutex
	selected *fleet.Worker
}

func (s *session) get() *fleet.Worker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selected
}

func (s *session) set(w *fleet.Worker) {
	s.mu.Lock()
	s.selected = w
	s.mu.Unlock()
}

func (s *Supervisor) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"}, // local tool, bound to localhost
	})
	if err != nil {
		s.log.Warn("websocket accept failed", "err", err)
		return
	}
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sess := &session{}
	if first, ok := s.fleet.First(); ok {
		sess.set(first)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); defer cancel(); s.readLoop(ctx, c, sess) }()
	go func() { defer wg.Done(); defer cancel(); s.writeLoop(ctx, c, sess) }()
	wg.Wait()

	// Same contract as the single-vehicle console: a closed tab is a dead
	// producer, and each worker's deadman stops its own vehicle within 250 ms.
	// We deliberately do not send an explicit stop here.
	s.log.Info("console disconnected; each vehicle's deadman will stop it")
}

type supervisorInbound struct {
	Type    string `json:"type"`
	Vehicle string `json:"vehicle"`
}

func (s *Supervisor) readLoop(ctx context.Context, c *websocket.Conn, sess *session) {
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}

		var head supervisorInbound
		if err := json.Unmarshal(data, &head); err != nil {
			continue
		}

		switch head.Type {
		case "select":
			w, ok := s.fleet.Get(head.Vehicle)
			if !ok {
				s.log.Warn("select for unknown vehicle", "vehicle", head.Vehicle)
				continue
			}
			// No explicit stop for the vehicle being left. Relaying simply
			// ceases, and its deadman zeroes it within 250 ms — the same path
			// as a crashed browser, and therefore the one we want exercised.
			sess.set(w)
			s.log.Info("console selected vehicle", "vehicle", w.Identity().ID)

		case "estop":
			if err := s.fleet.EStopAll(ctx); err != nil {
				s.log.Error("E-STOP did not reach every vehicle", "err", err)
			} else {
				s.log.Warn("E-STOP from console — all vehicles")
			}

		case "clear_estop":
			if err := s.fleet.ClearEStopAll(ctx); err != nil {
				s.log.Error("clearing e-stop did not reach every vehicle", "err", err)
			} else {
				s.log.Info("e-stop cleared from console — all vehicles")
			}

		default:
			// cmd, fire, arm, disarm: relayed untouched to the selected
			// vehicle's governor, which is the only thing allowed to clamp.
			w := sess.get()
			if w == nil {
				continue
			}
			if err := w.Send(ctx, data); err != nil {
				s.log.Debug("relay failed", "vehicle", w.Identity().ID, "err", err)
			}
		}
	}
}

func (s *Supervisor) writeLoop(ctx context.Context, c *websocket.Conn, sess *session) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// A complete default shape, always. The HUD dereferences numeric fields
		// directly (t.cmdRateHz.toFixed(1)); a missing key would throw inside
		// the message handler and take the console's telemetry down with it,
		// which is exactly how a one-line slip blanked the whole console once
		// before. Defaults cost nothing and remove the whole class.
		payload := map[string]any{
			"type":        "tel",
			"vehicles":    s.vehicles(),
			"estopped":    s.fleet.EStopped(),
			"connected":   false,
			"battery":     0,
			"haveBattery": false,
			"armed":       false,
			"moving":      false,
			"reason":      "no vehicle",
			"frameAgeMs":  -1.0,
			"cmdRateHz":   0.0,
			"viewers":     0,
			"maxChassis":  0.0,
			"maxGimbal":   0.0,
		}

		w := sess.get()
		if w != nil {
			id := w.Identity()
			payload["vehicle"] = id.ID
			payload["vehicleName"] = id.Name
			payload["vehicleUp"] = w.Up()

			if t, seen := w.Telemetry(); t != nil {
				for k, v := range t {
					// "type" stays "tel"; "vehicles"/"estopped" are fleet-wide
					// and must not be overwritten by one worker's view.
					switch k {
					case "type", "vehicles", "estopped":
						continue
					}
					payload[k] = v
				}
				// A worker whose telemetry has gone stale is not reporting a
				// connected robot, whatever its last payload said.
				if time.Since(seen) > time.Second {
					payload["connected"] = false
					payload["stale"] = true
				}
			}
			if !w.Up() {
				payload["connected"] = false
			}
		}

		b, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			return
		}
	}
}
