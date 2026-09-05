package safety

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock lets the rules be tested by advancing time rather than sleeping,
// so the suite is deterministic and fast.
type fakeClock struct{ ns atomic.Int64 }

func newClock() *fakeClock {
	c := &fakeClock{}
	c.ns.Store(time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *fakeClock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

func drive(x, y float64) Command {
	return Command{Source: SourceHuman, ChassisX: x, ChassisY: y}
}

func newGov(t *testing.T, cfg Config) (*Governor, *fakeClock) {
	t.Helper()
	c := newClock()
	return NewWithClock(cfg, c.now), c
}

func TestZeroBeforeAnyCommand(t *testing.T) {
	g, _ := newGov(t, Config{})

	out := g.Tick()
	if out.Moving() || out.Reason != ReasonNoCommand {
		t.Fatalf("want stationary/%s, got %+v", ReasonNoCommand, out)
	}
}

func TestFreshCommandPassesThrough(t *testing.T) {
	g, _ := newGov(t, Config{})

	if err := g.Submit(drive(0.2, -0.3)); err != nil {
		t.Fatalf("submit: %v", err)
	}

	out := g.Tick()
	if out.Reason != ReasonOK {
		t.Fatalf("want %s, got %s", ReasonOK, out.Reason)
	}
	if out.ChassisX != 0.2 || out.ChassisY != -0.3 {
		t.Fatalf("command altered: %+v", out)
	}
}

// The deadman covers a producer that died. It is the rule that makes a dropped
// WebSocket safe rather than a runaway.
func TestDeadmanZeroesAfterSilence(t *testing.T) {
	g, clk := newGov(t, Config{Deadman: 250 * time.Millisecond, DefaultTTL: time.Hour})

	if err := g.Submit(drive(0.5, 0.5)); err != nil {
		t.Fatalf("submit: %v", err)
	}

	clk.advance(240 * time.Millisecond)
	if out := g.Tick(); !out.Moving() {
		t.Fatalf("stopped too early at 240ms: %+v", out)
	}

	clk.advance(20 * time.Millisecond) // now 260ms > 250ms
	out := g.Tick()
	if out.Moving() || out.Reason != ReasonDeadman {
		t.Fatalf("want stationary/%s, got %+v", ReasonDeadman, out)
	}
}

// TTL and the deadman are different failures: a stale command versus a dead
// producer. A long deadman must not rescue an expired command.
func TestTTLExpiryIsIndependentOfDeadman(t *testing.T) {
	g, clk := newGov(t, Config{Deadman: time.Hour})

	if err := g.Submit(Command{Source: SourceHuman, TTL: 50 * time.Millisecond, ChassisY: 0.4}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	clk.advance(60 * time.Millisecond)
	out := g.Tick()
	if out.Moving() || out.Reason != ReasonExpired {
		t.Fatalf("want stationary/%s, got %+v", ReasonExpired, out)
	}
}

// An expired command must decay to zero, never to "the last known good value".
func TestExpiryReturnsZeroNotLastValue(t *testing.T) {
	g, clk := newGov(t, Config{Deadman: 100 * time.Millisecond})

	_ = g.Submit(drive(0.9, 0.9))
	clk.advance(200 * time.Millisecond)

	out := g.Tick()
	if out.ChassisX != 0 || out.ChassisY != 0 {
		t.Fatalf("stale command leaked through: %+v", out)
	}
}

func TestClampLimitsDeflection(t *testing.T) {
	g, _ := newGov(t, Config{MaxChassis: 0.5, MaxGimbal: 0.4})

	_ = g.Submit(Command{Source: SourceHuman,
		ChassisX: 1.0, ChassisY: -1.0, GimbalX: 0.9, GimbalY: -0.9})

	out := g.Tick()
	if out.Reason != ReasonClamped {
		t.Fatalf("want %s, got %s", ReasonClamped, out.Reason)
	}
	if out.ChassisX != 0.5 || out.ChassisY != -0.5 {
		t.Fatalf("chassis not clamped both ways: %+v", out)
	}
	if out.GimbalX != 0.4 || out.GimbalY != -0.4 {
		t.Fatalf("gimbal not clamped both ways: %+v", out)
	}
}

// The console is never trusted to clamp; the governor is the authority.
func TestClampAppliesToIntentToo(t *testing.T) {
	g, _ := newGov(t, Config{MaxChassis: 0.3})

	_ = g.Submit(Command{Source: SourceIntent, ChassisY: 1.0})

	if out := g.Tick(); out.ChassisY != 0.3 {
		t.Fatalf("intent bypassed the clamp: %+v", out)
	}
}

func TestFireRequiresArming(t *testing.T) {
	g, _ := newGov(t, Config{})

	_ = g.Submit(Command{Source: SourceHuman, Fire: 1})
	if out := g.Tick(); out.Fire != 0 {
		t.Fatalf("fired while disarmed: %+v", out)
	}

	if err := g.Arm(SourceHuman); err != nil {
		t.Fatalf("arm: %v", err)
	}
	_ = g.Submit(Command{Source: SourceHuman, Fire: 1})
	if out := g.Tick(); out.Fire != 1 {
		t.Fatalf("armed human could not fire: %+v", out)
	}
}

// A command held for its whole TTL at a 20 Hz refresh must not become a burst.
func TestFireIsOneShotPerCommand(t *testing.T) {
	g, _ := newGov(t, Config{})
	_ = g.Arm(SourceHuman)

	_ = g.Submit(Command{Source: SourceHuman, Fire: 1})

	if out := g.Tick(); out.Fire != 1 {
		t.Fatalf("first tick should fire: %+v", out)
	}
	for i := 0; i < 5; i++ {
		if out := g.Tick(); out.Fire != 0 {
			t.Fatalf("tick %d re-fired the same command: %+v", i+2, out)
		}
	}
}

func TestArmingExpires(t *testing.T) {
	g, clk := newGov(t, Config{ArmTTL: 30 * time.Second, Deadman: time.Hour, DefaultTTL: time.Hour})
	_ = g.Arm(SourceHuman)

	clk.advance(31 * time.Second)
	if g.Armed() {
		t.Fatal("arming outlived its TTL")
	}

	_ = g.Submit(Command{Source: SourceHuman, Fire: 1})
	if out := g.Tick(); out.Fire != 0 {
		t.Fatalf("fired on lapsed arming: %+v", out)
	}
}

// The autonomy loop cannot arm itself. This is the whole reason Source exists.
func TestIntentCannotArm(t *testing.T) {
	g, _ := newGov(t, Config{})

	if err := g.Arm(SourceIntent); err == nil {
		t.Fatal("intent was allowed to arm")
	}
	if g.Armed() {
		t.Fatal("failed arm still armed the governor")
	}
}

// DECISIONS.md #7: no model actuates the blaster in v1, even on a human's arming.
func TestIntentCannotFireEvenWhenArmed(t *testing.T) {
	g, _ := newGov(t, Config{})
	_ = g.Arm(SourceHuman)

	_ = g.Submit(Command{Source: SourceIntent, Fire: 3})
	if out := g.Tick(); out.Fire != 0 {
		t.Fatalf("intent fired in v1 configuration: %+v", out)
	}
}

// ...and the escape hatch is a deliberate config flag, so the restriction is a
// decision rather than an accident of implementation.
func TestIntentFireRequiresExplicitOptIn(t *testing.T) {
	g, _ := newGov(t, Config{AllowIntentFire: true})
	_ = g.Arm(SourceHuman)

	_ = g.Submit(Command{Source: SourceIntent, Fire: 2})
	if out := g.Tick(); out.Fire != 2 {
		t.Fatalf("opt-in did not permit intent fire: %+v", out)
	}
}

func TestEStopHoldsUntilCleared(t *testing.T) {
	g, _ := newGov(t, Config{})

	_ = g.Submit(drive(0.5, 0.5))
	g.EStop()

	for i := 0; i < 3; i++ {
		out := g.Tick()
		if out.Moving() || out.Reason != ReasonEStop {
			t.Fatalf("e-stop released itself on tick %d: %+v", i, out)
		}
	}

	// Commands arriving during the hold are refused outright, so there is no
	// backlog to execute when it lifts.
	if err := g.Submit(drive(0.5, 0.5)); !errors.Is(err, ErrEStopped) {
		t.Fatalf("want ErrEStopped, got %v", err)
	}
	if out := g.Tick(); out.Moving() {
		t.Fatalf("command executed during e-stop: %+v", out)
	}

	g.ClearEStop()
	if out := g.Tick(); out.Moving() {
		t.Fatalf("clearing e-stop resurrected a command: %+v", out)
	}

	_ = g.Submit(drive(0.1, 0.1))
	if out := g.Tick(); !out.Moving() {
		t.Fatalf("governor did not resume after e-stop cleared: %+v", out)
	}
}

func TestEStopDisarms(t *testing.T) {
	g, _ := newGov(t, Config{})
	_ = g.Arm(SourceHuman)

	g.EStop()
	g.ClearEStop()

	if g.Armed() {
		t.Fatal("arming survived an e-stop")
	}
}

func TestCannotArmWhileEStopped(t *testing.T) {
	g, _ := newGov(t, Config{})
	g.EStop()

	if err := g.Arm(SourceHuman); err == nil {
		t.Fatal("armed while e-stopped")
	}
}

// ARCHITECTURE.md §5.5: a reconnect must never silently restore a live blaster.
func TestLinkLossZeroesAndDisarms(t *testing.T) {
	g, _ := newGov(t, Config{})
	_ = g.Arm(SourceHuman)
	_ = g.Submit(drive(0.4, 0.4))

	g.SetLinkDown(true)
	out := g.Tick()
	if out.Moving() || out.Reason != ReasonLinkDown {
		t.Fatalf("want stationary/%s, got %+v", ReasonLinkDown, out)
	}
	if g.Armed() {
		t.Fatal("link loss left the blaster armed")
	}

	if err := g.Submit(drive(0.4, 0.4)); !errors.Is(err, ErrLinkDown) {
		t.Fatalf("want ErrLinkDown, got %v", err)
	}

	g.SetLinkDown(false)
	if out := g.Tick(); out.Moving() {
		t.Fatalf("reconnect resurrected the pre-loss command: %+v", out)
	}
	if g.Armed() {
		t.Fatal("reconnect restored arming")
	}
}

func TestValidateRejectsBadCommands(t *testing.T) {
	cases := map[string]Command{
		"empty source":   {},
		"unknown source": {Source: "robot"},
		"negative TTL":   {Source: SourceHuman, TTL: -time.Second},
		"negative fire":  {Source: SourceHuman, Fire: -1},
		"NaN axis":       {Source: SourceHuman, ChassisX: math.NaN()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c.Validate(); err == nil {
				t.Fatalf("accepted invalid command: %+v", c)
			}
			g, _ := newGov(t, Config{})
			if err := g.Submit(c); err == nil {
				t.Fatal("governor accepted an invalid command")
			}
			if out := g.Tick(); out.Moving() {
				t.Fatalf("invalid command moved the robot: %+v", out)
			}
		})
	}
}

func TestDefaultsAreTheDocumentedOnes(t *testing.T) {
	cfg := New(Config{}).Config()
	if cfg.Deadman != DefaultDeadman || cfg.ArmTTL != DefaultArmTTL {
		t.Fatalf("defaults drifted from ARCHITECTURE.md §5: %+v", cfg)
	}
	if cfg.AllowIntentFire {
		t.Fatal("intent fire must default to off (DECISIONS.md #7)")
	}
}

// Producers submit from their own goroutines while the driver loop ticks.
// Run with -race.
func TestConcurrentSubmitAndTick(t *testing.T) {
	g, _ := newGov(t, Config{Deadman: time.Hour, DefaultTTL: time.Hour})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				_ = g.Submit(drive(0.1, 0.1))
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				g.Tick()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = g.Arm(SourceHuman)
				g.Disarm()
				g.EStop()
				g.ClearEStop()
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
