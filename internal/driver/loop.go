// Package driver runs the control loop: tick the safety governor at a fixed
// rate and put whatever it permits on the wire.
//
// The loop is deliberately hardware-free. It drives a Sink, so the rules can be
// exercised in tests without a robot — which matters more here than usual,
// because commands through the DJI bridge are never acknowledged and a passing
// call proves nothing (DECISIONS.md #12).
package driver

import (
	"context"
	"time"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/safety"
)

// DefaultRate is the control refresh. The S1 watchdogs its virtual stick, so
// the loop sends every tick including the zeros: a lapsed stream is not a stop
// command, it is undefined behaviour.
const DefaultRate = 50 * time.Millisecond

// Sink is whatever actually moves. The real one wraps the DJI client; tests use
// a recorder.
type Sink interface {
	// Move applies stick deflections in [-1, 1].
	Move(chassisX, chassisY, gimbalX, gimbalY float64) error
	// Fire discharges the blaster. Only ever called when the governor has
	// permitted it, which requires arming and an authorised source.
	Fire(rounds int) error
}

// Options configure a Run.
type Options struct {
	// Rate is the tick interval. Zero means DefaultRate.
	Rate time.Duration
	// OnTick observes every output the loop sends. Optional; used by the
	// telemetry feed and by the hardware safety demo.
	OnTick func(safety.Output)
	// OnError is called when a Sink call fails. Optional. Errors are never
	// fatal to the loop: the robot is safer with a loop that keeps sending
	// zeros than with one that exits.
	OnError func(error)
}

// Run ticks until ctx is done, then sends one final zero so the vehicle is not
// left holding the last command while the watchdog runs down.
func Run(ctx context.Context, gov *safety.Governor, sink Sink, opts Options) {
	rate := opts.Rate
	if rate <= 0 {
		rate = DefaultRate
	}

	ticker := time.NewTicker(rate)
	defer ticker.Stop()

	report := func(err error) {
		if err != nil && opts.OnError != nil {
			opts.OnError(err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			report(sink.Move(0, 0, 0, 0))
			return
		case <-ticker.C:
			out := gov.Tick()

			if opts.OnTick != nil {
				opts.OnTick(out)
			}

			report(sink.Move(out.ChassisX, out.ChassisY, out.GimbalX, out.GimbalY))

			if out.Fire > 0 {
				report(sink.Fire(out.Fire))
			}
		}
	}
}
