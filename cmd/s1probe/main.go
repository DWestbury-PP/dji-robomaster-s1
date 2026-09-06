// Command s1probe is the M1 latency harness: it connects to a RoboMaster S1
// through the app-mode UnityBridge, measures the numbers that ARCHITECTURE.md
// §6 leaves blank, and writes a JSON report.
//
// It deliberately measures only what software honestly can. True key-to-motion
// and glass-to-glass latency need external instrumentation (film the screen and
// the robot together); see docs/M1.md. What this gives you is the control-plane
// round trip, the video pipeline's throughput and jitter, and the CPU cost of
// decoding inside an emulated library — which is the number DECISIONS.md #11
// says we must have before reconsidering the arm64 work.
//
// Safety: by default this never translates the chassis. RTT actuation probing
// uses the gimbal, which rotates in place. Chassis motion is opt-in with
// -allow-chassis, and even then is a brief nudge at the slowest speed level.
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/brunoga/robomaster"
	"github.com/brunoga/robomaster/module/camera"
	"github.com/brunoga/robomaster/module/gimbal"
	"github.com/brunoga/robomaster/support/logger"
	"github.com/brunoga/robomaster/unitybridge/unity/key"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/probe"
)

var (
	appID       = flag.Uint64("appid", 0, "App ID to connect to. 0 connects to the first robot found.")
	wifiDirect  = flag.Bool("wifi-direct", false, "Connect via WiFi Direct (robot as its own AP) instead of router mode. Router mode is the default.")
	videoSecs   = flag.Duration("video", 30*time.Second, "How long to sample the video stream.")
	rttSamples  = flag.Int("rtt-samples", 30, "Control-plane round trips to measure.")
	allowChass  = flag.Bool("allow-chassis", false, "Permit a brief chassis nudge for actuation RTT. Off by default: the gimbal is used instead.")
	jsonOut     = flag.String("json", "", "Write the full report as JSON to this path.")
	connectOnly = flag.Bool("connect-only", false, "Connect, report link facts, exit. No video, no motion.")
	safetyRun   = flag.Bool("safety-demo", false, "Prove the safety layer on hardware: drive, let the producer die and watch the deadman stop it, then e-stop mid-drive. Implies motion.")
	motion      = flag.Bool("motion", false, "Run a bounded motion exercise during video sampling: rotation in place, sub-metre translations, gimbal sweeps, infrared fire. Never fires beads.")
	verbose     = flag.Bool("v", false, "Verbose library logging.")
)

func main() {
	flag.Parse()

	rep := &probe.Report{
		StartedAt: time.Now(),
		Host: probe.Host{
			GOARCH: runtime.GOARCH, GOOS: runtime.GOOS,
			UnderRosetta: runtime.GOARCH == "amd64" && isAppleSilicon(),
			GoVersion:    runtime.Version(),
		},
		WiFiMode:   map[bool]string{true: "wifi-direct", false: "router"}[*wifiDirect],
		Incomplete: true,
	}

	wallStart := time.Now()
	defer func() {
		rep.CPU = cpuUsage(wallStart)
		printReport(rep)
		if *jsonOut != "" {
			if err := rep.WriteJSON(*jsonOut); err != nil {
				fmt.Fprintf(os.Stderr, "\nwriting json: %v\n", err)
			} else {
				fmt.Printf("\nJSON report: %s\n", *jsonOut)
			}
		}
	}()

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelDebug
	}
	l := logger.New(level)

	// ---- Phase 1: connect -------------------------------------------------
	fmt.Println("connecting...")
	t0 := time.Now()

	var (
		c   *robomaster.Client
		err error
	)
	if *wifiDirect {
		c, err = robomaster.NewWifiDirect(l)
	} else {
		c, err = robomaster.New(l, *appID)
	}
	if err != nil {
		fatal(rep, "creating client: %v", err)
		return
	}

	if err := c.Start(); err != nil {
		fatal(rep, "starting client: %v", err)
		return
	}
	defer func() {
		if err := c.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "stopping client: %v\n", err)
		}
	}()

	if !c.Robot().WaitForDevices(20 * time.Second) {
		fatal(rep, "robot did not report its devices within 20s — is it powered on and on the same network?")
		return
	}
	rep.ConnectMs = float64(time.Since(t0).Nanoseconds()) / 1e6
	fmt.Printf("connected in %.0f ms\n", rep.ConnectMs)

	rep.Notes = append(rep.Notes,
		"battery at start: "+batteryString(c),
		fmt.Sprintf("devices: %v", c.Robot().Devices()))

	if *connectOnly {
		rep.Incomplete = false
		return
	}

	// ---- Safety demonstration (M2) ----------------------------------------
	// Runs instead of the measurement phases: it is a proof, not a benchmark.
	if *safetyRun {
		fmt.Println("safety layer demonstration — the robot will move")
		sr := &safetyReport{}
		if err := safetyDemo(c, sr); err != nil {
			fatal(rep, "safety demo: %v", err)
			return
		}
		sr.DeadmanFiredAfterMs = float64(sr.DeadmanFiredAfter.Nanoseconds()) / 1e6
		sr.print()

		rep.Notes = append(rep.Notes,
			fmt.Sprintf("safety: moved=%v stopped_on_silence=%v deadman=%.0fms estop_stopped=%v refused=%d accepted=%d no_lurch=%v",
				sr.MovedUnderCommand, sr.StoppedAfterSilence, sr.DeadmanFiredAfterMs,
				sr.StoppedOnEStop, sr.RefusedDuringEStop, sr.AcceptedDuringEStop,
				sr.StillStoppedAfterClear))
		rep.Safety = sr
		rep.Incomplete = false
		return
	}

	// ---- Phase 2: control-plane RTT ---------------------------------------
	// Robot.ChassisSpeedLevel() calls GetKeyValueSync(k, useCache=true), which
	// returns a cached value without touching the network — it measures a map
	// lookup, not the link. We go to the bridge directly with useCache=false to
	// force a real request/response, and keep the cached path as a control so
	// the difference between the two is visible in the report.
	//
	// Even forced, this is a LOWER BOUND on key-to-motion: actuation adds
	// mechanical delay no software timing can see (docs/M1.md).
	fmt.Printf("measuring control-plane RTT (%d samples)...\n", *rttSamples)
	rtt := probe.NewSeries("control_plane_rtt_wire")
	cached := probe.NewSeries("control_plane_cached")
	ub := c.Robot().UB()
	for i := 0; i < *rttSamples; i++ {
		s := time.Now()
		if _, err := ub.GetKeyValueSync(key.KeyRobomasterSystemChassisSpeedLevel, false); err != nil {
			rep.Notes = append(rep.Notes, fmt.Sprintf("wire rtt sample %d failed: %v", i, err))
		} else {
			rtt.Add(time.Since(s))
		}

		s = time.Now()
		if _, err := c.Robot().ChassisSpeedLevel(); err == nil {
			cached.Add(time.Since(s))
		}

		time.Sleep(50 * time.Millisecond)
	}
	rep.Series = append(rep.Series, rtt.Summary(), cached.Summary())

	// ---- Phase 3: actuation probe -----------------------------------------
	if err := actuationProbe(c, rep, *allowChass); err != nil {
		rep.Notes = append(rep.Notes, fmt.Sprintf("actuation probe: %v", err))
	}

	// ---- Phase 4: video, optionally under motion --------------------------
	// The jitter figure only means something when the motors are running and
	// the antenna is moving, so the exercise runs concurrently with sampling.
	if *motion {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					rep.Notes = append(rep.Notes, fmt.Sprintf("motion program panicked: %v", r))
				}
			}()
			if err := motionProgram(ctx, c, func(f string, a ...any) {
				fmt.Printf(f+"\n", a...)
			}); err != nil {
				rep.Notes = append(rep.Notes, fmt.Sprintf("motion program: %v", err))
			}
		}()
		rep.Notes = append(rep.Notes, "video sampled WHILE MOVING (-motion)")

		err := videoProbe(c, rep, *videoSecs)
		cancel()
		<-done
		if err != nil {
			fatal(rep, "video probe: %v", err)
			return
		}
	} else {
		if err := videoProbe(c, rep, *videoSecs); err != nil {
			fatal(rep, "video probe: %v", err)
			return
		}
	}

	rep.Notes = append(rep.Notes, "battery at end: "+batteryString(c))
	rep.Incomplete = false
}

// actuationProbe times a real command reaching real hardware. The gimbal is
// used because it rotates in place: nothing drives off a table.
func actuationProbe(c *robomaster.Client, rep *probe.Report, allowChassis bool) error {
	fmt.Println("measuring actuation command latency (gimbal, in place)...")

	g := c.Gimbal()
	series := probe.NewSeries("gimbal_command_issue")

	for i := 0; i < 10; i++ {
		yaw := int16(20)
		if i%2 == 1 {
			yaw = -20
		}
		s := time.Now()
		if err := g.SetRotationSpeed(0, yaw); err != nil {
			return fmt.Errorf("gimbal rotate: %w", err)
		}
		series.Add(time.Since(s))
		time.Sleep(250 * time.Millisecond)
	}
	if err := g.StopRotation(); err != nil {
		return fmt.Errorf("gimbal stop: %w", err)
	}
	if err := g.ResetPosition(); err != nil {
		rep.Notes = append(rep.Notes, fmt.Sprintf("gimbal reset: %v", err))
	}
	rep.Series = append(rep.Series, series.Summary())

	if allowChassis {
		rep.Notes = append(rep.Notes, "chassis nudge enabled by -allow-chassis")
		fmt.Println("  chassis nudge (opt-in)...")
		ch := c.Chassis()
		if err := ch.SetSpeed(chassisMode, 0.1, 0, 0); err != nil {
			return fmt.Errorf("chassis nudge: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
		if err := ch.StopMovement(chassisMode); err != nil {
			return fmt.Errorf("chassis stop: %w", err)
		}
	}
	_ = gimbal.Axis(0) // keep the gimbal import honest across API revisions
	return nil
}

// videoProbe samples the stream and measures three things: how long the first
// frame takes, how evenly frames arrive (the jitter that decides whether the
// link is usable under motion), and what it costs us to turn a frame into a
// JPEG for the browser and the perception tiers.
func videoProbe(c *robomaster.Client, rep *probe.Report, d time.Duration) error {
	fmt.Printf("sampling video for %s...\n", d)

	var (
		mu        sync.Mutex
		frames    int
		w, h      int
		last      time.Time
		firstSeen time.Time
		interval  = probe.NewSeries("video_inter_frame")
		encode    = probe.NewSeries("jpeg_encode")
	)

	start := time.Now()
	token, err := c.Camera().AddVideoCallback(func(frame *camera.RGB) {
		now := time.Now()
		mu.Lock()
		defer mu.Unlock()

		if frames == 0 {
			firstSeen = now
			b := frame.Bounds()
			w, h = b.Dx(), b.Dy()
		} else {
			interval.Add(now.Sub(last))
		}
		last = now
		frames++

		// Sample the encode cost rather than paying it every frame; measuring
		// it on every frame would itself distort the interval measurement.
		if frames%15 == 0 {
			s := time.Now()
			if err := jpeg.Encode(io.Discard, toRGBA(frame), &jpeg.Options{Quality: 80}); err == nil {
				encode.Add(time.Since(s))
			}
		}
	})
	if err != nil {
		return fmt.Errorf("adding video callback: %w", err)
	}
	defer c.Camera().RemoveVideoCallback(token)

	time.Sleep(d)

	mu.Lock()
	defer mu.Unlock()

	if frames == 0 {
		return fmt.Errorf("no frames received in %s — camera did not stream", d)
	}

	elapsed := last.Sub(firstSeen).Seconds()
	fps := 0.0
	if elapsed > 0 {
		fps = float64(frames-1) / elapsed
	}

	rep.FirstFrame = float64(firstSeen.Sub(start).Nanoseconds()) / 1e6
	rep.Video = probe.Video{
		Frames: frames, Width: w, Height: h,
		DurationSec: elapsed, FPS: fps,
		FPSDeficit: (nominalFPS - fps) / nominalFPS * 100,
	}
	rep.Series = append(rep.Series, interval.Summary(), encode.Summary())
	return nil
}

// toRGBA converts the bridge's RGB frame to an image.RGBA without going through
// the per-pixel At() path, which would dominate the measurement.
func toRGBA(src *camera.RGB) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := 0; y < b.Dy(); y++ {
		si := y * src.Stride
		di := y * dst.Stride
		for x := 0; x < b.Dx(); x++ {
			dst.Pix[di+0] = src.Pix[si+0]
			dst.Pix[di+1] = src.Pix[si+1]
			dst.Pix[di+2] = src.Pix[si+2]
			dst.Pix[di+3] = 255
			si += 3
			di += 4
		}
	}
	return dst
}

func cpuUsage(wallStart time.Time) probe.CPU {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	user := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
	sys := float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
	wall := time.Since(wallStart).Seconds()
	cores := 0.0
	if wall > 0 {
		cores = (user + sys) / wall
	}
	return probe.CPU{UserSec: user, SystemSec: sys, WallSec: wall, CoresBusy: cores}
}

// safeBattery reads the battery percentage defensively. Upstream's
// BatteryPowerPercent dereferences an atomic pointer that stays nil until the
// robot pushes its first battery event, so calling it too early panics. We poll
// briefly and recover rather than crash a measurement run over a nice-to-have.
func safeBattery(c *robomaster.Client) (pct uint8, ok bool) {
	read := func() (p uint8, good bool) {
		defer func() {
			if recover() != nil {
				p, good = 0, false
			}
		}()
		return c.Robot().BatteryPowerPercent(), true
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p, good := read(); good {
			return p, true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, false
}

func batteryString(c *robomaster.Client) string {
	if pct, ok := safeBattery(c); ok {
		return fmt.Sprintf("%d%%", pct)
	}
	return "unavailable (robot had not pushed a battery event yet)"
}

func fatal(rep *probe.Report, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	rep.Notes = append(rep.Notes, "FAILED: "+msg)
	fmt.Fprintf(os.Stderr, "\nerror: %s\n", msg)
}

func isAppleSilicon() bool {
	// Under Rosetta the process reports amd64 while the machine is arm64.
	// sysctl.proc_translated is the authoritative signal.
	v, err := syscall.SysctlUint32("sysctl.proc_translated")
	return err == nil && v == 1
}
