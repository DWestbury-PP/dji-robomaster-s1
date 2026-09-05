package teleop

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/safety"
)

func newTestServer(t *testing.T, cfg safety.Config) (*httptest.Server, *Server, *safety.Governor) {
	t.Helper()
	gov := safety.New(cfg)
	hub := NewFrameHub(60)
	s := New(Config{StatusFn: func() Status {
		return Status{Connected: true, Battery: 88, HaveBatt: true}
	}}, gov, hub)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, s, gov
}

func dial(t *testing.T, ts *httptest.Server) (*websocket.Conn, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c, ctx, cancel
}

func send(t *testing.T, c *websocket.Conn, ctx context.Context, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readUntil collects telemetry until pred matches or the budget runs out.
func readUntil(t *testing.T, c *websocket.Conn, ctx context.Context,
	pred func(telemetry) bool) (telemetry, bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, data, err := c.Read(ctx)
		if err != nil {
			return telemetry{}, false
		}
		var tel telemetry
		if err := json.Unmarshal(data, &tel); err != nil {
			continue
		}
		if pred(tel) {
			return tel, true
		}
	}
	return telemetry{}, false
}

// A command from the browser must reach the governor and be reflected back in
// telemetry — the console's only feedback that it is actually in control.
func TestCommandReachesGovernor(t *testing.T) {
	ts, _, gov := newTestServer(t, safety.Config{Deadman: time.Hour, DefaultTTL: time.Hour, MaxChassis: 0.35})
	c, ctx, cancel := dial(t, ts)
	defer cancel()

	send(t, c, ctx, map[string]any{"type": "cmd", "chassisY": 0.2})

	if _, ok := readUntil(t, c, ctx, func(tl telemetry) bool { return tl.CmdRateHz > 0 }); !ok {
		t.Fatal("command never showed up in telemetry")
	}
	if out := gov.Tick(); out.ChassisY != 0.2 {
		t.Fatalf("governor did not receive the command: %+v", out)
	}
}

// The browser is never trusted to clamp. Over-range input must be limited
// server-side, whatever the console sends.
func TestOverRangeCommandIsClampedServerSide(t *testing.T) {
	ts, _, gov := newTestServer(t, safety.Config{Deadman: time.Hour, DefaultTTL: time.Hour, MaxChassis: 0.35})
	c, ctx, cancel := dial(t, ts)
	defer cancel()

	send(t, c, ctx, map[string]any{"type": "cmd", "chassisY": 1.0})
	if _, ok := readUntil(t, c, ctx, func(tl telemetry) bool { return tl.CmdRateHz > 0 }); !ok {
		t.Fatal("command never arrived")
	}

	out := gov.Tick()
	if out.ChassisY != 0.35 {
		t.Fatalf("console bypassed the clamp: %+v", out)
	}
	if out.Reason != safety.ReasonClamped {
		t.Fatalf("clamping was not reported: %s", out.Reason)
	}
}

func TestEStopFromConsoleRefusesFurtherCommands(t *testing.T) {
	ts, _, gov := newTestServer(t, safety.Config{Deadman: time.Hour, DefaultTTL: time.Hour})
	c, ctx, cancel := dial(t, ts)
	defer cancel()

	send(t, c, ctx, map[string]any{"type": "estop"})
	if _, ok := readUntil(t, c, ctx, func(tl telemetry) bool { return tl.EStopped }); !ok {
		t.Fatal("e-stop never reflected in telemetry")
	}

	// A console that has not noticed keeps streaming; none of it may stick.
	send(t, c, ctx, map[string]any{"type": "cmd", "chassisY": 0.5})
	time.Sleep(200 * time.Millisecond)
	if out := gov.Tick(); out.Moving() {
		t.Fatalf("command executed during e-stop: %+v", out)
	}

	send(t, c, ctx, map[string]any{"type": "clear_estop"})
	if _, ok := readUntil(t, c, ctx, func(tl telemetry) bool { return !tl.EStopped }); !ok {
		t.Fatal("e-stop never cleared")
	}
	if out := gov.Tick(); out.Moving() {
		t.Fatalf("clearing the e-stop resurrected a command: %+v", out)
	}
}

func TestArmAndFireRoundTrip(t *testing.T) {
	ts, _, gov := newTestServer(t, safety.Config{Deadman: time.Hour, DefaultTTL: time.Hour})
	c, ctx, cancel := dial(t, ts)
	defer cancel()

	// Fire while disarmed must not produce a shot.
	send(t, c, ctx, map[string]any{"type": "fire"})
	time.Sleep(150 * time.Millisecond)
	if out := gov.Tick(); out.Fire != 0 {
		t.Fatalf("fired while disarmed: %+v", out)
	}

	send(t, c, ctx, map[string]any{"type": "arm"})
	if _, ok := readUntil(t, c, ctx, func(tl telemetry) bool { return tl.Armed }); !ok {
		t.Fatal("arming never reflected in telemetry")
	}

	send(t, c, ctx, map[string]any{"type": "fire"})
	time.Sleep(150 * time.Millisecond)
	if out := gov.Tick(); out.Fire != 1 {
		t.Fatalf("armed console could not fire: %+v", out)
	}

	send(t, c, ctx, map[string]any{"type": "disarm"})
	if _, ok := readUntil(t, c, ctx, func(tl telemetry) bool { return !tl.Armed }); !ok {
		t.Fatal("disarm never reflected")
	}
}

// Closing the console is a dead producer. Nothing special happens here on
// purpose: the deadman covers it, which is the same path as a browser crash.
func TestConsoleDisconnectLeavesDeadmanToStop(t *testing.T) {
	ts, _, gov := newTestServer(t, safety.Config{Deadman: 100 * time.Millisecond})
	c, ctx, cancel := dial(t, ts)
	defer cancel()

	send(t, c, ctx, map[string]any{"type": "cmd", "chassisY": 0.3})
	if _, ok := readUntil(t, c, ctx, func(tl telemetry) bool { return tl.CmdRateHz > 0 }); !ok {
		t.Fatal("command never arrived")
	}
	c.CloseNow()

	time.Sleep(200 * time.Millisecond)
	out := gov.Tick()
	if out.Moving() || out.Reason != safety.ReasonDeadman {
		t.Fatalf("want stationary/%s after disconnect, got %+v", safety.ReasonDeadman, out)
	}
}

func TestTelemetryCarriesVehicleStatus(t *testing.T) {
	ts, _, _ := newTestServer(t, safety.Config{})
	c, ctx, cancel := dial(t, ts)
	defer cancel()

	tel, ok := readUntil(t, c, ctx, func(tl telemetry) bool { return tl.Type == "tel" })
	if !ok {
		t.Fatal("no telemetry")
	}
	if !tel.Connected || tel.Battery != 88 || !tel.HaveBatt {
		t.Fatalf("status not passed through: %+v", tel)
	}
}

func TestUIAndHealthEndpoints(t *testing.T) {
	ts, _, _ := newTestServer(t, safety.Config{})

	res, err := ts.Client().Get(ts.URL + "/")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("UI not served: %v %v", err, res)
	}
	res.Body.Close()

	res, err = ts.Client().Get(ts.URL + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, res)
	}
	res.Body.Close()
}

// The hub must encode once and hand the same bytes to every viewer.
func TestFrameHubEncodesOnceAndServesLatest(t *testing.T) {
	hub := NewFrameHub(70)
	stop := make(chan struct{})
	defer close(stop)
	go hub.Run(stop, 60)

	pix := make([]byte, 32*24*3)
	for i := range pix {
		pix[i] = byte(i)
	}
	hub.Submit(pix, 32, 24)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if frame, _ := hub.Latest(); len(frame) > 0 {
			if frame[0] != 0xFF || frame[1] != 0xD8 {
				t.Fatalf("not a JPEG: % x", frame[:4])
			}
			if age := hub.AgeMs(); age < 0 {
				t.Fatal("age not recorded")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("hub never produced a frame")
}

// Undersized or empty submissions must be ignored, not panic the video path.
func TestFrameHubRejectsMalformedFrames(t *testing.T) {
	hub := NewFrameHub(70)
	hub.Submit(nil, 0, 0)
	hub.Submit([]byte{1, 2, 3}, 64, 64) // far too short for the dimensions
	if frame, _ := hub.Latest(); len(frame) != 0 {
		t.Fatal("malformed frame was accepted")
	}
}
