package teleop

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/experience"
	"github.com/DWestbury-PP/dji-robomaster-s1/internal/safety"
)

//go:embed ui.html
var uiFS embed.FS

// Status is the vehicle state the HUD shows. Supplied by the caller because
// this package deliberately knows nothing about the DJI client.
type Status struct {
	Connected bool
	Battery   int
	HaveBatt  bool
}

// Config configures a Server.
type Config struct {
	Addr      string // listen address, e.g. "localhost:8700"
	StreamFPS int    // browser video rate; encode cost is ~23.6 ms/frame
	Quality   int    // JPEG quality
	StatusFn  func() Status
	Log       *slog.Logger
}

// Server is the browser console. Commands arrive over the WebSocket and go
// straight into the governor — the browser never talks to the robot, and is
// never trusted to clamp anything (ARCHITECTURE.md §5).
type Server struct {
	cfg Config
	gov *safety.Governor
	hub *FrameHub
	log *slog.Logger

	lastReason atomic.Value // safety.Reason
	moving     atomic.Bool

	cmdCount atomic.Uint64
	viewers  atomic.Int64

	perception *perceptionStore

	// rec is optional. When present every control tick, observation and vehicle
	// change is recorded; when nil the calls are no-ops.
	rec *experience.Recorder

	reqMu      sync.Mutex
	lastReq    [4]float64
	lastFire   int
	loggedReq  [4]float64
	loggedApp  [4]float64
	loggedAt   time.Time
	loggedOnce bool
}

// SetRecorder attaches a drive recorder. Safe to leave unset.
func (s *Server) SetRecorder(r *experience.Recorder) { s.rec = r }

func New(cfg Config, gov *safety.Governor, hub *FrameHub) *Server {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.StreamFPS <= 0 {
		cfg.StreamFPS = 15
	}
	s := &Server{cfg: cfg, gov: gov, hub: hub, log: cfg.Log, perception: newPerceptionStore()}
	s.lastReason.Store(safety.ReasonNoCommand)
	return s
}

// ObserveTick is wired to the driver loop so the HUD can show *why* the robot
// is doing what it is doing. The governor names a reason on every output, which
// turns "it won't move" into "deadman" without a debugger.
func (s *Server) ObserveTick(out safety.Output) {
	s.lastReason.Store(out.Reason)
	s.moving.Store(out.Moving())

	if s.rec == nil {
		return
	}

	// The control signal is a step function, so recording every change plus a
	// slow heartbeat replays exactly while costing a fraction of the disk of a
	// full 20 Hz dump.
	applied := [4]float64{out.ChassisX, out.ChassisY, out.GimbalX, out.GimbalY}

	s.reqMu.Lock()
	req, fire := s.lastReq, s.lastFire
	s.lastFire = 0
	changed := !s.loggedOnce || req != s.loggedReq || applied != s.loggedApp || fire > 0
	stale := time.Since(s.loggedAt) > time.Second
	if changed || stale {
		s.loggedReq, s.loggedApp, s.loggedAt, s.loggedOnce = req, applied, time.Now(), true
		s.reqMu.Unlock()
		s.rec.Control("human", req, applied, string(out.Reason), fire)
		return
	}
	s.reqMu.Unlock()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleUI)
	mux.HandleFunc("GET /stream", s.handleStream)
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("GET /frame.jpg", s.handleFrame)
	mux.HandleFunc("POST /perception", s.handlePerception)
	mux.HandleFunc("POST /perception/pending", s.handlePending)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	b, err := uiFS.ReadFile("ui.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// handleStream writes multipart MJPEG. Each viewer blocks on the hub's update
// channel, so a slow client cannot make the encoder wait.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	const boundary = "s1frame"

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")

	s.viewers.Add(1)
	defer s.viewers.Add(-1)

	ctx := r.Context()
	for {
		frame, updated := s.hub.Latest()
		if len(frame) > 0 {
			if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n",
				boundary, len(frame)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := w.Write([]byte("\r\n")); err != nil {
				return
			}
			flusher.Flush()
		}

		select {
		case <-ctx.Done():
			return
		case <-updated:
		case <-time.After(2 * time.Second):
			// No frames at all: keep the connection warm rather than dropping
			// it, so the browser does not thrash reconnecting.
		}
	}
}

// inbound is what the browser sends.
type inbound struct {
	Type     string  `json:"type"`
	ChassisX float64 `json:"chassisX"`
	ChassisY float64 `json:"chassisY"`
	GimbalX  float64 `json:"gimbalX"`
	GimbalY  float64 `json:"gimbalY"`
}

// telemetry is what the HUD renders.
type telemetry struct {
	Type       string  `json:"type"`
	Connected  bool    `json:"connected"`
	Battery    int     `json:"battery"`
	HaveBatt   bool    `json:"haveBattery"`
	Armed      bool    `json:"armed"`
	EStopped   bool    `json:"estopped"`
	Moving     bool    `json:"moving"`
	Reason     string  `json:"reason"`
	FrameAgeMs float64 `json:"frameAgeMs"`
	CmdRateHz  float64 `json:"cmdRateHz"`
	Viewers    int64   `json:"viewers"`
	MaxChassis float64 `json:"maxChassis"`
	MaxGimbal  float64 `json:"maxGimbal"`
	// Perception is the newest observation per tier, each dated so the console
	// can show how old it is rather than implying it is current.
	Perception map[string]dated `json:"perception,omitempty"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
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

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); defer cancel(); s.readLoop(ctx, c) }()
	go func() { defer wg.Done(); defer cancel(); s.writeLoop(ctx, c) }()
	wg.Wait()

	// A closed console is a dead producer. We do not stop the robot here
	// explicitly — the deadman does it within 250 ms, which is the same path
	// that covers a browser crash or a Wi-Fi drop, and therefore the path we
	// want exercised (ARCHITECTURE.md §5.1).
	s.log.Info("console disconnected; deadman will stop the vehicle")
}

func (s *Server) readLoop(ctx context.Context, c *websocket.Conn) {
	for {
		var msg inbound
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			s.log.Warn("bad message from console", "err", err)
			continue
		}

		switch msg.Type {
		case "cmd":
			cmd := safety.Command{
				Source:   safety.SourceHuman,
				ChassisX: msg.ChassisX,
				ChassisY: msg.ChassisY,
				GimbalX:  msg.GimbalX,
				GimbalY:  msg.GimbalY,
			}
			if err := s.gov.Submit(cmd); err != nil {
				// Refusals are expected during an e-stop and are not errors
				// worth logging at every 20 Hz tick.
				continue
			}
			s.cmdCount.Add(1)

			// Remember what the human asked for, so the recorder can log it
			// alongside what the governor allowed.
			s.reqMu.Lock()
			s.lastReq = [4]float64{msg.ChassisX, msg.ChassisY, msg.GimbalX, msg.GimbalY}
			s.reqMu.Unlock()

		case "fire":
			s.reqMu.Lock()
			s.lastFire = 1
			s.reqMu.Unlock()
			// Fire rides along with the current stick position so releasing
			// the trigger does not also stop the vehicle.
			_ = s.gov.Submit(safety.Command{
				Source:   safety.SourceHuman,
				ChassisX: msg.ChassisX,
				ChassisY: msg.ChassisY,
				GimbalX:  msg.GimbalX,
				GimbalY:  msg.GimbalY,
				Fire:     1,
			})

		case "arm":
			if err := s.gov.Arm(safety.SourceHuman); err != nil {
				s.log.Warn("arm refused", "err", err)
			}
		case "disarm":
			s.gov.Disarm()
		case "estop":
			s.gov.EStop()
			s.log.Warn("E-STOP from console")
		case "clear_estop":
			s.gov.ClearEStop()
			s.log.Info("e-stop cleared from console")
		}
	}
}

func (s *Server) writeLoop(ctx context.Context, c *websocket.Conn) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	// Sample the command rate over a full second. A 100 ms window sees either
	// two commands or none at 20 Hz, which makes the HUD flip between 20 and 0
	// and tells the operator nothing.
	lastCount := s.cmdCount.Load()
	lastAt := time.Now()
	rate := 0.0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if now := time.Now(); now.Sub(lastAt) >= time.Second {
				count := s.cmdCount.Load()
				rate = float64(count-lastCount) / now.Sub(lastAt).Seconds()
				lastCount, lastAt = count, now
			}

			var st Status
			if s.cfg.StatusFn != nil {
				st = s.cfg.StatusFn()
			}
			cfg := s.gov.Config()

			t := telemetry{
				Type:       "tel",
				Connected:  st.Connected,
				Battery:    st.Battery,
				HaveBatt:   st.HaveBatt,
				Armed:      s.gov.Armed(),
				EStopped:   s.gov.EStopped(),
				Moving:     s.moving.Load(),
				Reason:     string(s.lastReason.Load().(safety.Reason)),
				FrameAgeMs: s.hub.AgeMs(),
				CmdRateHz:  rate,
				Viewers:    s.viewers.Load(),
				MaxChassis: cfg.MaxChassis,
				MaxGimbal:  cfg.MaxGimbal,
				Perception: s.perception.snapshot(),
			}

			b, err := json.Marshal(t)
			if err != nil {
				continue
			}
			wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err = c.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
