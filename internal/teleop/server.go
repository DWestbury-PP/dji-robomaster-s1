package teleop

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
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

	// VehicleID and VehicleName identify which robot this process drives, for
	// a supervisor multiplexing several. Operator-supplied rather than derived
	// from the robot: the bridge does not hand us the MAC, and a name the
	// operator chose reads better in a dropdown than a hardware address.
	VehicleID   string
	VehicleName string
	AppID       uint64

	// SetLED, when present, sets this vehicle's armour LEDs. Injected as a
	// function so this package stays free of DJI specifics, the same reason
	// StatusFn is supplied rather than a client.
	SetLED func(opts map[string]int, r, g, b uint8, effect bool) error

	// SetLEDRaw sends an arbitrary JSON payload to the LED key. It exists only
	// to identify the undocumented wire format, and is wired up only when the
	// operator passes -led-experiment.
	//
	// It is not a feature and must not become one. The native library parses
	// this payload with json_dto and throws a C++ exception on a malformed
	// one, which is uncaught and aborts the process — Go cannot recover. An
	// always-on endpoint that crashes the vehicle on bad input is a remote
	// kill switch, so it stays behind a flag until the format is known.
	SetLEDRaw func(payload string) error
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
	mux.HandleFunc("GET /vehicle", s.handleVehicle)
	mux.HandleFunc("POST /led", s.handleLED)
	if s.cfg.SetLEDRaw != nil {
		mux.HandleFunc("POST /led/raw", s.handleLEDRaw)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// handleLED sets the vehicle's LEDs, so two robots driven from one console can
// be told apart by eye. The wire format is not documented and is still being
// identified against real hardware, hence the format selector.
//
// The reply says "sent", never "worked": the bridge does not acknowledge this,
// so claiming success here would be inventing evidence.
func (s *Server) handleLED(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SetLED == nil {
		http.Error(w, "no LED control on this vehicle", http.StatusNotImplemented)
		return
	}
	q := r.URL.Query()
	atoi := func(name string, def int) int {
		if v := q.Get(name); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		return def
	}
	clamp := func(n int) uint8 {
		if n < 0 {
			return 0
		}
		if n > 255 {
			return 255
		}
		return uint8(n)
	}

	opts := map[string]int{
		"deviceID":    atoi("deviceID", 0),
		"controlMode": atoi("controlMode", 1),
		"flashMode":   atoi("flashMode", 1),
		"loopCount":   atoi("loopCount", 0),
		"time1":       atoi("time1", 0),
		"time2":       atoi("time2", 0),
	}
	red, green, blue := clamp(atoi("r", 0)), clamp(atoi("g", 0)), clamp(atoi("b", 0))

	useEffect := q.Get("key") == "effect"
	if err := s.cfg.SetLED(opts, red, green, blue, useEffect); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sent": true, "opts": opts, "r": red, "g": green, "b": blue, "effectKey": useEffect,
		"note": "sent to the bridge; look at the vehicle to see whether it obeyed",
	})
}

// handleLEDRaw is an experiment, not an endpoint. See Config.SetLEDRaw: a
// malformed payload aborts this process from inside the DJI library.
func (s *Server) handleLEDRaw(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	if err := s.cfg.SetLEDRaw(string(body)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fmt.Fprintln(w, "sent — check the vehicle, and check this process is still alive")
}

// handleVehicle answers "which robot are you holding?" for a supervisor. Safe
// to call on a single-vehicle console too, where it simply describes itself.
func (s *Server) handleVehicle(w http.ResponseWriter, r *http.Request) {
	id, name := s.cfg.VehicleID, s.cfg.VehicleName
	if id == "" {
		id = s.cfg.Addr
	}
	if name == "" {
		name = id
	}
	var connected bool
	if s.cfg.StatusFn != nil {
		connected = s.cfg.StatusFn().Connected
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":        id,
		"name":      name,
		"appID":     s.cfg.AppID,
		"connected": connected,
	})
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
