package main

import (
	"context"
	"fmt"
	"time"

	"github.com/brunoga/robomaster"
	"github.com/brunoga/robomaster/module/controller"
	"github.com/brunoga/robomaster/module/gun"
	"github.com/brunoga/robomaster/module/robot"
)

// Movement goes through the Controller module's virtual stick
// (KeyMainControllerVirtualStick), which is the path the DJI mobile app uses.
// Chassis.SetSpeed() writes a different key and the S1 silently ignores it —
// DirectSendKeyValue is fire-and-forget, so it returns nil either way. That
// cost us a run; see docs/M1.md.
//
// Stick values are fractions of full deflection in [-1, 1], NOT m/s.
const (
	// Office-sized deflection. A quarter stick is a walking pace.
	maxDeflection = 0.25
	// The vehicle watchdogs the virtual stick: commands must be refreshed or
	// motion never starts, and stops if the stream lapses. This is the hardware
	// independently confirming DECISIONS.md #3 — rate commands under a deadman.
	refreshInterval = 50 * time.Millisecond
	legDuration     = 1200 * time.Millisecond
	restBetween     = 400 * time.Millisecond
)

// motionProgram drives a bounded, repeating exercise while video is sampled, so
// inter-frame jitter is measured with the motors running and the antenna
// orientation changing. Returns when ctx is cancelled.
func motionProgram(ctx context.Context, c *robomaster.Client, log func(string, ...any)) error {
	r := c.Robot()

	if err := r.EnableFunction(robot.FunctionTypeMovementControl, true); err != nil {
		return fmt.Errorf("enabling movement control: %w", err)
	}
	if err := r.EnableFunction(robot.FunctionTypeGunControl, true); err != nil {
		log("gun control unavailable: %v", err)
	}

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

	ctrl := c.Controller()

	// Whatever happens — cancel, error, panic — the sticks go to centre. Same
	// principle the safety layer enforces in M2 (ARCHITECTURE.md §5).
	defer func() { _ = center(ctrl) }()

	type leg struct {
		name               string
		chassisX, chassisY float64 // strafe, forward/back
		gimbalX, gimbalY   float64 // yaw, pitch
		fire               bool
	}

	// Translations are paired so the vehicle returns to roughly where it began.
	lap := []leg{
		{name: "forward", chassisY: maxDeflection},
		{name: "back", chassisY: -maxDeflection},
		{name: "strafe left", chassisX: -maxDeflection},
		{name: "strafe right", chassisX: maxDeflection},
		{name: "rotate left (yaw, chassis follows)", gimbalX: -0.30},
		{name: "rotate right (yaw, chassis follows)", gimbalX: 0.30},
		{name: "gimbal pitch up", gimbalY: 0.30},
		{name: "gimbal pitch down", gimbalY: -0.30},
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
					log("    fire: %v", err)
				}
				if !sleepCtx(ctx, restBetween) {
					return nil
				}
				continue
			}

			if err := holdStick(ctx, ctrl,
				clamp(l.chassisX), clamp(l.chassisY),
				clamp(l.gimbalX), clamp(l.gimbalY), legDuration); err != nil {
				log("    move: %v", err)
			}

			_ = center(ctrl)
			if !sleepCtx(ctx, restBetween) {
				return nil
			}
		}
	}
}

// holdStick refreshes the virtual stick at refreshInterval for d. A single send
// is not enough: the vehicle expects a continuous stream and ignores or expires
// anything less.
func holdStick(ctx context.Context, ctrl *controller.Controller,
	cx, cy, gx, gy float64, d time.Duration) error {
	deadline := time.Now().Add(d)
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		chassis := controller.StickPosition{X: cx, Y: cy}
		gimbal := controller.StickPosition{X: gx, Y: gy}
		if err := ctrl.Move(&chassis, &gimbal, controller.ModeFPV); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

// center returns both sticks to neutral.
func center(ctrl *controller.Controller) error {
	zero := controller.StickPosition{X: 0, Y: 0}
	return ctrl.Move(&zero, &zero, controller.ModeFPV)
}

// fireBurst uses infrared only. TypeBead launches physical gel projectiles and
// is deliberately never referenced — DECISIONS.md #7 keeps the blaster out of
// automated paths, and an office is not where we relax that.
func fireBurst(c *robomaster.Client) error {
	if !c.Gun().WaitForConnection(2 * time.Second) {
		return fmt.Errorf("gun module not connected")
	}
	return c.Gun().Fire(gun.TypeInfrared)
}

func clamp(v float64) float64 {
	if v > maxDeflection*1.2 {
		return maxDeflection * 1.2
	}
	if v < -maxDeflection*1.2 {
		return -maxDeflection * 1.2
	}
	return v
}

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
