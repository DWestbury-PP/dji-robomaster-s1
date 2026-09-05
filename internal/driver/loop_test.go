package driver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/safety"
)

type recorder struct {
	mu       sync.Mutex
	moves    [][4]float64
	fires    []int
	failMove error
}

func (r *recorder) Move(cx, cy, gx, gy float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.moves = append(r.moves, [4]float64{cx, cy, gx, gy})
	return r.failMove
}

func (r *recorder) Fire(n int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fires = append(r.fires, n)
	return nil
}

func (r *recorder) snapshot() ([][4]float64, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := make([][4]float64, len(r.moves))
	copy(m, r.moves)
	f := make([]int, len(r.fires))
	copy(f, r.fires)
	return m, f
}

func runFor(t *testing.T, gov *safety.Governor, sink Sink, opts Options, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); Run(ctx, gov, sink, opts) }()
	time.Sleep(d)
	cancel()
	<-done
}

// The loop must send on every tick, including when the governor says stop.
// Silence would leave the vehicle's own watchdog to guess.
func TestSendsEveryTickIncludingZeros(t *testing.T) {
	gov := safety.New(safety.Config{})
	r := &recorder{}

	runFor(t, gov, r, Options{Rate: 10 * time.Millisecond}, 120*time.Millisecond)

	moves, _ := r.snapshot()
	if len(moves) < 5 {
		t.Fatalf("expected repeated sends, got %d", len(moves))
	}
	for i, m := range moves {
		if m != [4]float64{0, 0, 0, 0} {
			t.Fatalf("move %d was not zero with no command submitted: %v", i, m)
		}
	}
}

func TestAppliesSubmittedCommand(t *testing.T) {
	gov := safety.New(safety.Config{Deadman: time.Hour, DefaultTTL: time.Hour})
	r := &recorder{}
	_ = gov.Submit(safety.Command{Source: safety.SourceHuman, ChassisY: 0.4, GimbalX: -0.2})

	runFor(t, gov, r, Options{Rate: 10 * time.Millisecond}, 60*time.Millisecond)

	moves, _ := r.snapshot()
	var sawCommand bool
	for _, m := range moves {
		if m[1] == 0.4 && m[2] == -0.2 {
			sawCommand = true
		}
	}
	if !sawCommand {
		t.Fatalf("command never reached the sink: %v", moves)
	}
}

// Cancelling must leave the vehicle stopped, not holding the last command.
func TestFinalZeroOnShutdown(t *testing.T) {
	gov := safety.New(safety.Config{Deadman: time.Hour, DefaultTTL: time.Hour})
	r := &recorder{}
	_ = gov.Submit(safety.Command{Source: safety.SourceHuman, ChassisY: 0.5})

	runFor(t, gov, r, Options{Rate: 10 * time.Millisecond}, 50*time.Millisecond)

	moves, _ := r.snapshot()
	last := moves[len(moves)-1]
	if last != [4]float64{0, 0, 0, 0} {
		t.Fatalf("loop exited without stopping the robot: %v", last)
	}
}

func TestFireOnlyWhenGovernorPermits(t *testing.T) {
	gov := safety.New(safety.Config{Deadman: time.Hour, DefaultTTL: time.Hour})
	r := &recorder{}

	// Disarmed: the request must not reach the sink at all.
	_ = gov.Submit(safety.Command{Source: safety.SourceHuman, Fire: 1})
	runFor(t, gov, r, Options{Rate: 10 * time.Millisecond}, 40*time.Millisecond)
	if _, fires := r.snapshot(); len(fires) != 0 {
		t.Fatalf("fired while disarmed: %v", fires)
	}

	// Armed: exactly once, however many ticks the command survives.
	_ = gov.Arm(safety.SourceHuman)
	_ = gov.Submit(safety.Command{Source: safety.SourceHuman, Fire: 1})
	runFor(t, gov, r, Options{Rate: 10 * time.Millisecond}, 60*time.Millisecond)
	if _, fires := r.snapshot(); len(fires) != 1 {
		t.Fatalf("want exactly one fire, got %v", fires)
	}
}

// A failing sink must not stop the loop: continuing to send zeros is safer
// than exiting and leaving the watchdog to time out.
func TestSinkErrorsDoNotStopTheLoop(t *testing.T) {
	gov := safety.New(safety.Config{})
	r := &recorder{failMove: errors.New("bridge unhappy")}

	var errCount int
	var mu sync.Mutex
	runFor(t, gov, r, Options{
		Rate:    10 * time.Millisecond,
		OnError: func(error) { mu.Lock(); errCount++; mu.Unlock() },
	}, 60*time.Millisecond)

	moves, _ := r.snapshot()
	if len(moves) < 3 {
		t.Fatalf("loop gave up after errors: %d sends", len(moves))
	}
	mu.Lock()
	defer mu.Unlock()
	if errCount == 0 {
		t.Fatal("errors were swallowed without reporting")
	}
}

func TestOnTickObservesOutputs(t *testing.T) {
	gov := safety.New(safety.Config{})
	r := &recorder{}

	var mu sync.Mutex
	reasons := map[safety.Reason]int{}
	runFor(t, gov, r, Options{
		Rate:   10 * time.Millisecond,
		OnTick: func(o safety.Output) { mu.Lock(); reasons[o.Reason]++; mu.Unlock() },
	}, 50*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if reasons[safety.ReasonNoCommand] == 0 {
		t.Fatalf("expected no-command ticks, saw %v", reasons)
	}
}
