package main

import (
	"context"
	"fmt"
	"time"

	"github.com/brunoga/robomaster"
	"github.com/brunoga/robomaster/module/gun"
	"github.com/brunoga/robomaster/module/robot"
)

// Office-sized limits. The point of the motion pass is to stress the radio and
// run the motors, not to cover ground: the vehicle should stay near where it
// started. These are hard ceilings, not suggestions — nothing below constructs
// a speed from user input.
const (
	maxTranslate = 0.25 // m/s   — the library permits 3.5
	maxRotate    = 45.0 // deg/s — the library permits 360
	legDuration  = 900 * time.Millisecond
	restBetween  = 350 * time.Millisecond
)

// motionProgram drives a bounded, repeating exercise while video is being
// sampled, so inter-frame jitter is measured with the motors running and the
// antenna orientation changing. It returns when ctx is cancelled.
//
// Everything it does is authorized-and-bounded: rotation in place, sub-metre
// translations in each direction (the Mecanum strafe included), gimbal sweeps,
// and infrared fire. It never fires beads — see fireBurst.
func motionProgram(ctx context.Context, c *robomaster.Client, log func(string, ...any)) error {
	r := c.Robot()

	if err := r.EnableFunction(robot.FunctionTypeMovementControl, true); err != nil {
		return fmt.Errorf("enabling movement control: %w", err)
	}
	if err := r.EnableFunction(robot.FunctionTypeGunControl, true); err != nil {
		log("gun control unavailable: %v", err)
	}

	// Cache and restore the operator's speed level; run the exercise on Slow.
	prevLevel, levelErr := r.ChassisSpeedLevel()
	if levelErr == nil {
		if err := r.SetChassisSpeedLevel(robot.ChassisSpeedLevelSlow); err != nil {
			log("could not set slow speed level: %v", err)
		}
		defer func() {
			if err := r.SetChassisSpeedLevel(prevLevel); err != nil {
				log("restoring speed level: %v", err)
			}
		}()
	}

	ch := c.Chassis()
	g := c.Gimbal()

	// Whatever happens, the vehicle stops. This is the same principle the
	// safety layer will enforce in M2 (ARCHITECTURE.md §5), applied early.
	defer func() {
		_ = ch.StopMovement(chassisMode)
		_ = g.StopRotation()
	}()

	type leg struct {
		name       string
		x, y, z    float64
		pitch, yaw int16
		fire       bool
	}

	// One lap of the exercise. Translations are paired so the vehicle returns
	// roughly to where it started rather than walking across the room.
	lap := []leg{
		{name: "rotate left", z: -maxRotate},
		{name: "rotate right", z: maxRotate},
		{name: "forward", x: maxTranslate},
		{name: "back", x: -maxTranslate},
		{name: "strafe left", y: -maxTranslate},
		{name: "strafe right", y: maxTranslate},
		{name: "gimbal yaw sweep", yaw: 40},
		{name: "gimbal yaw return", yaw: -40},
		{name: "gimbal pitch up", pitch: 25},
		{name: "gimbal pitch down", pitch: -25},
		{name: "infrared fire", fire: true},
	}

	for lapNum := 1; ; lapNum++ {
		for _, l := range lap {
			if ctx.Err() != nil {
				return nil
			}
			log("  lap %d: %s", lapNum, l.name)

			if l.fire {
				if err := fireBurst(c); err != nil {
					log("  fire: %v", err)
				}
				if !sleepCtx(ctx, restBetween) {
					return nil
				}
				continue
			}

			if l.x != 0 || l.y != 0 || l.z != 0 {
				if err := ch.SetSpeed(chassisMode, clamp(l.x, maxTranslate),
					clamp(l.y, maxTranslate), clamp(l.z, maxRotate)); err != nil {
					log("  chassis: %v", err)
				}
			}
			if l.pitch != 0 || l.yaw != 0 {
				if err := g.SetRotationSpeed(l.pitch, l.yaw); err != nil {
					log("  gimbal: %v", err)
				}
			}

			if !sleepCtx(ctx, legDuration) {
				return nil
			}

			_ = ch.StopMovement(chassisMode)
			_ = g.StopRotation()

			if !sleepCtx(ctx, restBetween) {
				return nil
			}
		}
	}
}

// fireBurst uses infrared only. TypeBead launches physical gel projectiles and
// is deliberately never referenced here — DECISIONS.md #7 keeps the blaster out
// of automated paths, and an office is not the place to relax that.
func fireBurst(c *robomaster.Client) error {
	if !c.Gun().WaitForConnection(2 * time.Second) {
		return fmt.Errorf("gun module not connected")
	}
	return c.Gun().Fire(gun.TypeInfrared)
}

func clamp(v, limit float64) float64 {
	if v > limit {
		return limit
	}
	if v < -limit {
		return -limit
	}
	return v
}

// sleepCtx sleeps unless the context ends first. Returns false if it ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
