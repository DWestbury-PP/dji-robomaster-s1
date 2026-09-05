package safety

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Commands are refused outright while the governor is holding. A producer that
// keeps streaming during an e-stop — the teleop console has no way to know —
// must not have its backlog execute the instant the hold is released.
var (
	ErrEStopped = errors.New("command rejected: e-stop active")
	ErrLinkDown = errors.New("command rejected: link down")
)

// Defaults. These are the numbers docs/ARCHITECTURE.md §5 commits to.
const (
	DefaultDeadman    = 250 * time.Millisecond
	DefaultTTL        = 250 * time.Millisecond
	DefaultArmTTL     = 30 * time.Second
	DefaultMaxChassis = 0.60
	DefaultMaxGimbal  = 0.60
)

// Config bounds what the governor will permit. Deflection limits are fractions
// of full stick travel, per DECISIONS.md #12.
type Config struct {
	// Deadman is how long a producer may be silent before rates go to zero.
	Deadman time.Duration
	// DefaultTTL applies to commands that do not carry their own.
	DefaultTTL time.Duration
	// ArmTTL is how long an arming action lasts before it lapses on its own.
	ArmTTL time.Duration

	MaxChassis float64
	MaxGimbal  float64

	// AllowIntentFire lets the autonomy loop shoot. It is false in v1 and
	// flipping it is a decision with its own sign-off, not a config tweak
	// (DECISIONS.md #7).
	AllowIntentFire bool
}

func (c Config) withDefaults() Config {
	if c.Deadman <= 0 {
		c.Deadman = DefaultDeadman
	}
	if c.DefaultTTL <= 0 {
		c.DefaultTTL = DefaultTTL
	}
	if c.ArmTTL <= 0 {
		c.ArmTTL = DefaultArmTTL
	}
	if c.MaxChassis <= 0 {
		c.MaxChassis = DefaultMaxChassis
	}
	if c.MaxGimbal <= 0 {
		c.MaxGimbal = DefaultMaxGimbal
	}
	return c
}

// Governor is the last thing between a command and the robot. It is safe for
// concurrent use: producers Submit from their own goroutines while the driver
// loop calls Tick at the refresh rate.
//
// The clock is injectable so the rules can be tested deterministically rather
// than with sleeps.
type Governor struct {
	cfg Config
	now func() time.Time

	mu         sync.Mutex
	have       bool
	cmd        Command
	cmdAt      time.Time
	pendingFir int
	armedUntil time.Time
	estop      bool
	linkDown   bool
}

// New returns a Governor. A zero Config gets the documented defaults.
func New(cfg Config) *Governor {
	return &Governor{cfg: cfg.withDefaults(), now: time.Now}
}

// NewWithClock is New with an injectable clock, for tests.
func NewWithClock(cfg Config, now func() time.Time) *Governor {
	g := New(cfg)
	g.now = now
	return g
}

// Config returns the effective configuration, defaults applied.
func (g *Governor) Config() Config { return g.cfg }

// Submit records the newest command. Older commands are simply replaced: the
// governor applies the freshest non-expired one and never queues.
func (g *Governor) Submit(c Command) error {
	if err := c.Validate(); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// Refuse rather than latch. Accepting here would mean the newest command
	// received during a hold fires the moment the hold ends, which is the
	// lurch-on-release the e-stop exists to prevent.
	if g.estop {
		return ErrEStopped
	}
	if g.linkDown {
		return ErrLinkDown
	}

	g.cmd = c
	g.cmdAt = g.now()
	g.have = true

	// Fire is latched here and consumed by exactly one Tick, so holding a
	// command for 250 ms at a 20 Hz refresh cannot turn one trigger pull into
	// five shots.
	if c.Fire > 0 {
		g.pendingFir = c.Fire
	}
	return nil
}

// Arm permits blaster fire for ArmTTL. Only a human may arm — an autonomy loop
// cannot arm itself, which is the point of tracking Source at all.
func (g *Governor) Arm(by Source) error {
	if by != SourceHuman {
		return fmt.Errorf("only %s may arm, not %s", SourceHuman, by)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.estop {
		return fmt.Errorf("cannot arm while e-stopped")
	}
	g.armedUntil = g.now().Add(g.cfg.ArmTTL)
	return nil
}

// Disarm revokes arming immediately.
func (g *Governor) Disarm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armedUntil = time.Time{}
	g.pendingFir = 0
}

// Armed reports whether fire would currently be permitted for a human.
func (g *Governor) Armed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armedLocked()
}

func (g *Governor) armedLocked() bool {
	return !g.armedUntil.IsZero() && g.now().Before(g.armedUntil)
}

// EStop zeroes everything and holds until ClearEStop. It also disarms: coming
// back from an emergency stop should never leave a live blaster.
func (g *Governor) EStop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.estop = true
	g.armedUntil = time.Time{}
	g.pendingFir = 0
	g.have = false
}

// ClearEStop releases the hold. It does not restore arming or the previous
// command: the operator must ask again for both.
func (g *Governor) ClearEStop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.estop = false
}

// EStopped reports whether the hold is active.
func (g *Governor) EStopped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.estop
}

// SetLinkDown marks the bridge or telemetry as lost. Like the deadman it zeroes
// output, and it disarms — a reconnect must never silently restore a live
// blaster (ARCHITECTURE.md §5.5).
func (g *Governor) SetLinkDown(down bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.linkDown = down
	if down {
		g.armedUntil = time.Time{}
		g.pendingFir = 0
		g.have = false
	}
}

// Tick returns what may go on the wire now. The driver loop calls this at the
// refresh rate and sends the result unconditionally — including the zeros,
// because the S1 watchdogs its virtual stick and silence is not a stop command
// as far as a half-delivered stream is concerned.
func (g *Governor) Tick() Output {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()

	switch {
	case g.estop:
		return Zero(ReasonEStop)
	case g.linkDown:
		return Zero(ReasonLinkDown)
	case !g.have:
		return Zero(ReasonNoCommand)
	}

	// The deadman covers a producer that stopped sending. It is deliberately
	// separate from the command's own TTL, which covers a command that is
	// simply too old to act on.
	if now.Sub(g.cmdAt) > g.cfg.Deadman {
		return Zero(ReasonDeadman)
	}

	ttl := g.cmd.TTL
	if ttl == 0 {
		ttl = g.cfg.DefaultTTL
	}
	if now.Sub(g.cmdAt) > ttl {
		return Zero(ReasonExpired)
	}

	out := Output{Reason: ReasonOK}
	var clamped bool

	out.ChassisX, clamped = clampTo(g.cmd.ChassisX, g.cfg.MaxChassis, clamped)
	out.ChassisY, clamped = clampTo(g.cmd.ChassisY, g.cfg.MaxChassis, clamped)
	out.GimbalX, clamped = clampTo(g.cmd.GimbalX, g.cfg.MaxGimbal, clamped)
	out.GimbalY, clamped = clampTo(g.cmd.GimbalY, g.cfg.MaxGimbal, clamped)

	if clamped {
		out.Reason = ReasonClamped
	}

	// Fire is the most restricted path in the system: it needs a pending
	// request, an unexpired arming, and an authority permitted to shoot.
	if g.pendingFir > 0 && g.armedLocked() && g.fireAllowedLocked() {
		out.Fire = g.pendingFir
		g.pendingFir = 0
	}

	return out
}

func (g *Governor) fireAllowedLocked() bool {
	if g.cmd.Source == SourceHuman {
		return true
	}
	return g.cfg.AllowIntentFire
}

// clampTo limits v to ±limit, reporting whether any clamping has happened yet.
func clampTo(v, limit float64, already bool) (float64, bool) {
	if v > limit {
		return limit, true
	}
	if v < -limit {
		return -limit, true
	}
	return v, already
}
