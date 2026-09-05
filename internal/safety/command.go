// Package safety enforces the rules in docs/ARCHITECTURE.md §5 on the last hop
// before the wire. It lives inside the process that holds the DJI bridge handle
// because there is no other path to the robot from there — a safety *service*
// would be a hop, and a hop can be routed around (DECISIONS.md #6).
//
// Units: every axis here is a fraction of full stick deflection in [-1, 1], not
// a velocity. The S1 is driven by a virtual stick and never accepted a speed in
// m/s (DECISIONS.md #12), so "speed clamp" means a deflection clamp.
package safety

import (
	"fmt"
	"time"
)

// Source is the authority behind a command. It is load-bearing, not
// decoration: a model may drive, but it may not shoot (DECISIONS.md #7).
type Source string

const (
	// SourceHuman is a person at a control surface — the teleop console today,
	// a mobile app later.
	SourceHuman Source = "human"
	// SourceIntent is the autonomy loop acting on foveate's fused world-belief.
	SourceIntent Source = "intent"
)

func (s Source) Valid() bool { return s == SourceHuman || s == SourceIntent }

// Command is a rate request with an explicit expiry. There is deliberately no
// position or waypoint form: under packet loss a rate decays safely to zero
// while a position keeps executing into whatever it was about to hit
// (DECISIONS.md #3).
type Command struct {
	Source Source
	// TTL bounds how long this command may be applied. Zero means the
	// governor's default. It covers a *stale command*; the deadman covers a
	// *dead producer*. Both are needed and they are not the same failure.
	TTL time.Duration

	ChassisX float64 // strafe: negative left, positive right
	ChassisY float64 // negative back, positive forward
	GimbalX  float64 // yaw: negative left, positive right
	GimbalY  float64 // pitch: negative down, positive up

	// Fire requests blaster rounds. Honoured only while armed, and never for
	// SourceIntent in v1. One command fires at most once however long it is
	// applied for.
	Fire int
}

func (c Command) Validate() error {
	if !c.Source.Valid() {
		return fmt.Errorf("invalid source %q", c.Source)
	}
	if c.TTL < 0 {
		return fmt.Errorf("negative TTL %s", c.TTL)
	}
	if c.Fire < 0 {
		return fmt.Errorf("negative fire count %d", c.Fire)
	}
	for name, v := range map[string]float64{
		"ChassisX": c.ChassisX, "ChassisY": c.ChassisY,
		"GimbalX": c.GimbalX, "GimbalY": c.GimbalY,
	} {
		if v != v { // NaN
			return fmt.Errorf("%s is NaN", name)
		}
	}
	return nil
}

// Output is what the governor permits on the wire this tick.
type Output struct {
	ChassisX, ChassisY float64
	GimbalX, GimbalY   float64
	Fire               int

	// Reason names the rule that produced this output. It is always set,
	// including when the command passed through untouched, so telemetry can
	// show *why* the robot is doing what it is doing.
	Reason Reason
}

// Moving reports whether this output actually commands motion.
func (o Output) Moving() bool {
	return o.ChassisX != 0 || o.ChassisY != 0 || o.GimbalX != 0 || o.GimbalY != 0
}

// Reason is why an output looks the way it does.
type Reason string

const (
	ReasonOK        Reason = "ok"          // command applied as given
	ReasonClamped   Reason = "clamped"     // applied, but limited
	ReasonNoCommand Reason = "no-command"  // nothing has ever been submitted
	ReasonDeadman   Reason = "deadman"     // producer went silent
	ReasonExpired   Reason = "ttl-expired" // command outlived its own TTL
	ReasonEStop     Reason = "estop"       // held until explicitly cleared
	ReasonLinkDown  Reason = "link-down"   // bridge or telemetry lost
)

// Zero is the safe output. Every failure path returns this — never the last
// known good value.
func Zero(r Reason) Output { return Output{Reason: r} }
