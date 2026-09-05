package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/brunoga/robomaster"
	"github.com/brunoga/robomaster/module/controller"
	"github.com/brunoga/robomaster/module/robot"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/safety"
)

// safetyRefresh is the driver loop rate. The S1 watchdogs its virtual stick, so
// the loop sends every tick — including the zeros. Silence is not a stop
// command (DECISIONS.md #12).
const safetyRefresh = 50 * time.Millisecond

// safetyDemo proves the governor on real hardware. Unit tests show the rules
// are correct; this shows they reach the motors.
//
// Three phases, each with an observable outcome:
//
//	A. commands stream at 20 Hz          → robot drives
//	B. producer goes silent              → deadman zeroes it, timed
//	C. e-stop mid-drive                  → immediate stop, and the backlog
//	                                       submitted during the hold is refused
func safetyDemo(c *robomaster.Client, rep *safetyReport) error {
	gov := safety.New(safety.Config{
		Deadman:    250 * time.Millisecond,
		MaxChassis: 0.30, // office-sized, and the console cannot raise it
		MaxGimbal:  0.30,
	})

	r := c.Robot()
	if err := r.EnableFunction(robot.FunctionTypeMovementControl, true); err != nil {
		return fmt.Errorf("enabling movement control: %w", err)
	}
	if lvl, err := r.ChassisSpeedLevel(); err == nil {
		_ = r.SetChassisSpeedLevel(robot.ChassisSpeedLevelSlow)
		defer func() { _ = r.SetChassisSpeedLevel(lvl) }()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	obs := &observer{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		driverLoop(ctx, gov, c.Controller(), obs)
	}()
	defer wg.Wait()

	// --- Phase A: a live producer -------------------------------------------
	fmt.Println("  A. streaming drive commands at 20 Hz for 2s (robot should move)")
	stopA := time.Now().Add(2 * time.Second)
	for time.Now().Before(stopA) {
		if err := gov.Submit(safety.Command{Source: safety.SourceHuman, ChassisY: 0.25}); err != nil {
			return fmt.Errorf("submit: %w", err)
		}
		time.Sleep(safetyRefresh)
	}
	rep.MovedUnderCommand = obs.sawMotion()

	// --- Phase B: the producer dies ----------------------------------------
	fmt.Println("  B. producer goes silent (deadman should stop it)")
	obs.markSilence()
	time.Sleep(1500 * time.Millisecond)

	rep.DeadmanFiredAfter = obs.deadmanLatency()
	rep.StoppedAfterSilence = !obs.sawMotionSinceSilence()

	// --- Phase C: e-stop mid-drive -----------------------------------------
	fmt.Println("  C. driving again, then e-stop mid-drive")
	stopC := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(stopC) {
		_ = gov.Submit(safety.Command{Source: safety.SourceHuman, ChassisY: 0.25})
		time.Sleep(safetyRefresh)
	}

	obs.markEStop()
	gov.EStop()

	// A console that has not noticed keeps streaming. Every one of these must
	// be refused, or they execute the instant the hold lifts.
	refused, accepted := 0, 0
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := gov.Submit(safety.Command{Source: safety.SourceHuman, ChassisY: 0.25}); err != nil {
			refused++
		} else {
			accepted++
		}
		time.Sleep(safetyRefresh)
	}
	rep.RefusedDuringEStop = refused
	rep.AcceptedDuringEStop = accepted
	rep.StoppedOnEStop = !obs.sawMotionSinceEStop()

	// Clearing must not resurrect anything.
	gov.ClearEStop()
	obs.markCleared()
	time.Sleep(400 * time.Millisecond)
	rep.StillStoppedAfterClear = !obs.sawMotionSinceCleared()

	cancel()
	return nil
}

// driverLoop is the shape s1-driver will have: tick the governor at a fixed
// rate and send whatever it permits, unconditionally.
func driverLoop(ctx context.Context, gov *safety.Governor,
	ctrl *controller.Controller, obs *observer) {
	ticker := time.NewTicker(safetyRefresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			zero := controller.StickPosition{}
			_ = ctrl.Move(&zero, &zero, controller.ModeFPV)
			return
		case <-ticker.C:
			out := gov.Tick()
			obs.record(out)

			chassis := controller.StickPosition{X: out.ChassisX, Y: out.ChassisY}
			gimbal := controller.StickPosition{X: out.GimbalX, Y: out.GimbalY}
			_ = ctrl.Move(&chassis, &gimbal, controller.ModeFPV)
		}
	}
}

// observer watches what the driver loop actually put on the wire, so the demo
// reports on real outputs rather than on what we believe we sent.
type observer struct {
	mu sync.Mutex

	anyMotion bool

	silenceAt  time.Time
	firstZero  time.Time
	motionPost bool

	estopAt       time.Time
	motionEStop   bool
	clearedAt     time.Time
	motionCleared bool
}

func (o *observer) record(out safety.Output) {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()
	moving := out.Moving()

	if moving {
		o.anyMotion = true
	}

	if !o.silenceAt.IsZero() {
		if moving {
			o.motionPost = true
		} else if o.firstZero.IsZero() {
			o.firstZero = now
		}
	}
	if !o.estopAt.IsZero() && moving {
		o.motionEStop = true
	}
	if !o.clearedAt.IsZero() && moving {
		o.motionCleared = true
	}
}

func (o *observer) markSilence() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.silenceAt = time.Now()
}

func (o *observer) markEStop() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.estopAt = time.Now()
}

func (o *observer) markCleared() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.clearedAt = time.Now()
}

func (o *observer) sawMotion() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.anyMotion
}

func (o *observer) sawMotionSinceSilence() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.motionPost
}

func (o *observer) sawMotionSinceEStop() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.motionEStop
}

func (o *observer) sawMotionSinceCleared() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.motionCleared
}

// deadmanLatency is how long after the producer went silent the wire first
// carried zeros. It should land near the configured deadman plus up to one
// tick of quantisation.
func (o *observer) deadmanLatency() time.Duration {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.silenceAt.IsZero() || o.firstZero.IsZero() {
		return 0
	}
	return o.firstZero.Sub(o.silenceAt)
}

type safetyReport struct {
	MovedUnderCommand      bool          `json:"moved_under_command"`
	DeadmanFiredAfter      time.Duration `json:"-"`
	DeadmanFiredAfterMs    float64       `json:"deadman_fired_after_ms"`
	StoppedAfterSilence    bool          `json:"stopped_after_silence"`
	StoppedOnEStop         bool          `json:"stopped_on_estop"`
	RefusedDuringEStop     int           `json:"refused_during_estop"`
	AcceptedDuringEStop    int           `json:"accepted_during_estop"`
	StillStoppedAfterClear bool          `json:"still_stopped_after_clear"`
}

func (s *safetyReport) print() {
	pass := func(b bool) string {
		if b {
			return "✅ pass"
		}
		return "❌ FAIL"
	}
	fmt.Println()
	fmt.Println("safety layer on hardware")
	fmt.Printf("  robot moved while commanded        %s\n", pass(s.MovedUnderCommand))
	fmt.Printf("  deadman stopped it after silence   %s  (fired after %.0f ms, configured 250 ms)\n",
		pass(s.StoppedAfterSilence), s.DeadmanFiredAfterMs)
	fmt.Printf("  e-stop stopped it mid-drive        %s\n", pass(s.StoppedOnEStop))
	fmt.Printf("  commands refused during e-stop     %s  (%d refused, %d accepted)\n",
		pass(s.AcceptedDuringEStop == 0 && s.RefusedDuringEStop > 0),
		s.RefusedDuringEStop, s.AcceptedDuringEStop)
	fmt.Printf("  no lurch when e-stop cleared       %s\n", pass(s.StillStoppedAfterClear))
}
