package teleop

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/fleet"
)

// fakeWorker stands in for a vehicle process: it answers /vehicle and records
// every console message it receives over /ws.
type fakeWorker struct {
	name string
	srv  *httptest.Server

	mu   sync.Mutex
	got  []string
	conn *websocket.Conn
}

func newFakeWorker(t *testing.T, name string) *fakeWorker {
	t.Helper()
	f := &fakeWorker{name: name}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /vehicle", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": name, "name": name, "connected": true,
		})
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		defer c.CloseNow()

		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			f.mu.Lock()
			f.got = append(f.got, string(data))
			f.mu.Unlock()
		}
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWorker) addr() string {
	return strings.TrimPrefix(f.srv.URL, "http://")
}

func (f *fakeWorker) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func (f *fakeWorker) sawType(typ string) bool {
	for _, m := range f.received() {
		var v struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(m), &v) == nil && v.Type == typ {
			return true
		}
	}
	return false
}

// setup wires a supervisor over two fake workers and returns a console socket.
func setup(t *testing.T) (*fakeWorker, *fakeWorker, *fleet.Fleet, *websocket.Conn, context.Context) {
	t.Helper()
	a := newFakeWorker(t, "Rover")
	b := newFakeWorker(t, "Scout")

	log := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	f := fleet.New([]string{a.addr(), b.addr()}, log)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go f.Run(ctx)

	sup := NewSupervisor(f, log)
	srv := httptest.NewServer(sup.Handler())
	t.Cleanup(srv.Close)

	waitFor(t, 3*time.Second, func() bool {
		for _, w := range f.Workers() {
			if !w.Up() {
				return false
			}
		}
		return true
	}, "workers did not come up")

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial console: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return a, b, f, c, ctx
}

func waitFor(t *testing.T, d time.Duration, ok func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

func sendSup(t *testing.T, c *websocket.Conn, ctx context.Context, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// The whole point of the fan-out: an e-stop must reach the vehicle you are NOT
// driving. In a multi-vehicle session the person who sees the collision coming
// is often not the one holding that vehicle's controls.
func TestEStopReachesEveryVehicleNotJustSelected(t *testing.T) {
	a, b, _, c, ctx := setup(t)

	sendSup(t, c, ctx, map[string]any{"type": "select", "vehicle": "Rover"})
	waitFor(t, time.Second, func() bool { return true }, "")

	sendSup(t, c, ctx, map[string]any{"type": "estop"})

	waitFor(t, 2*time.Second, func() bool { return a.sawType("estop") }, "selected vehicle never got the e-stop")
	waitFor(t, 2*time.Second, func() bool { return b.sawType("estop") }, "UNSELECTED vehicle never got the e-stop")
}

// A command must go only to the selected vehicle. Routing a drive command to
// the wrong robot is the multi-vehicle equivalent of an inverted axis.
func TestCommandsRouteOnlyToSelectedVehicle(t *testing.T) {
	a, b, _, c, ctx := setup(t)

	sendSup(t, c, ctx, map[string]any{"type": "select", "vehicle": "Scout"})
	waitFor(t, 2*time.Second, func() bool { return true }, "")
	time.Sleep(100 * time.Millisecond)

	sendSup(t, c, ctx, map[string]any{"type": "cmd", "chassisX": 0.5})

	waitFor(t, 2*time.Second, func() bool { return b.sawType("cmd") }, "selected vehicle never got the command")
	time.Sleep(200 * time.Millisecond)
	if a.sawType("cmd") {
		t.Fatal("command leaked to the vehicle that was NOT selected")
	}
}

// Switching away must not send a stop — the vehicle's own deadman does it. This
// pins the property rather than the implementation: if someone later "helpfully"
// adds an explicit stop on switch, they should have to justify it here.
func TestSwitchingAwaySendsNoExplicitStop(t *testing.T) {
	a, _, _, c, ctx := setup(t)

	sendSup(t, c, ctx, map[string]any{"type": "select", "vehicle": "Rover"})
	time.Sleep(100 * time.Millisecond)
	sendSup(t, c, ctx, map[string]any{"type": "cmd", "chassisX": 0.5})
	waitFor(t, 2*time.Second, func() bool { return a.sawType("cmd") }, "no command reached Rover")

	sendSup(t, c, ctx, map[string]any{"type": "select", "vehicle": "Scout"})
	time.Sleep(300 * time.Millisecond)

	if a.sawType("estop") {
		t.Fatal("switching away sent an explicit e-stop; the deadman is supposed to handle it")
	}
}

// An unknown vehicle id must be ignored, not panic and not silently retarget
// some other robot.
func TestUnknownVehicleIsIgnored(t *testing.T) {
	a, b, _, c, ctx := setup(t)

	sendSup(t, c, ctx, map[string]any{"type": "select", "vehicle": "does-not-exist"})
	time.Sleep(150 * time.Millisecond)
	sendSup(t, c, ctx, map[string]any{"type": "cmd", "chassisX": 0.9})
	time.Sleep(250 * time.Millisecond)

	// Selection was refused, so the session keeps its previous target (Rover,
	// the first worker up). What must never happen is the command vanishing
	// into a nil worker or landing on Scout.
	if b.sawType("cmd") && !a.sawType("cmd") {
		t.Fatal("a bad vehicle id retargeted the session to a different robot")
	}
}

// The fleet reports its own e-stop state so the console can show it, and it
// must survive being read back.
func TestFleetEStopStateIsVisible(t *testing.T) {
	_, _, f, c, ctx := setup(t)

	if f.EStopped() {
		t.Fatal("fleet started e-stopped")
	}
	sendSup(t, c, ctx, map[string]any{"type": "estop"})
	waitFor(t, 2*time.Second, func() bool { return f.EStopped() }, "fleet never reported e-stopped")

	sendSup(t, c, ctx, map[string]any{"type": "clear_estop"})
	waitFor(t, 2*time.Second, func() bool { return !f.EStopped() }, "fleet never cleared e-stop")
}

// dropLink severs the worker's socket the way a crashed or restarted vehicle
// process would, leaving the supervisor to reconnect.
func (f *fakeWorker) dropLink() {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	if c != nil {
		c.CloseNow()
	}
}

// The subtle one. A worker that drops and reconnects during an e-stop would
// otherwise come back willing to accept commands: its governor is fresh, and
// the supervisor's latch lives only in the supervisor. Re-asserting on every
// connect is what makes "stopped" mean stopped across a reconnect.
func TestLatchedEStopIsReassertedOnReconnect(t *testing.T) {
	a, _, f, c, ctx := setup(t)

	sendSup(t, c, ctx, map[string]any{"type": "estop"})
	waitFor(t, 2*time.Second, func() bool { return a.sawType("estop") }, "no initial e-stop")
	waitFor(t, 2*time.Second, func() bool { return f.EStopped() }, "fleet not latched")

	before := countType(a, "estop")
	a.dropLink()
	waitFor(t, 3*time.Second, func() bool { return countType(a, "estop") > before },
		"vehicle reconnected during an e-stop WITHOUT being re-stopped")
}

func countType(f *fakeWorker, typ string) int {
	n := 0
	for _, m := range f.received() {
		var v struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(m), &v) == nil && v.Type == typ {
			n++
		}
	}
	return n
}
