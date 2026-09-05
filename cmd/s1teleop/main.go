// Command s1teleop is the browser control console: it holds the DJI bridge,
// runs the safety governor and the control loop, and serves the UI.
//
// One process, because the bridge handle is process-wide and the frames are
// already here — routing video out to a separate service would cost a second
// JPEG encode (measured at 23.6 ms) on the most latency-sensitive path in the
// system. See DECISIONS.md #13.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/brunoga/robomaster"
	"github.com/brunoga/robomaster/module/camera"
	"github.com/brunoga/robomaster/module/controller"
	"github.com/brunoga/robomaster/module/gun"
	"github.com/brunoga/robomaster/module/robot"
	"github.com/brunoga/robomaster/support/logger"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/driver"
	"github.com/DWestbury-PP/dji-robomaster-s1/internal/safety"
	"github.com/DWestbury-PP/dji-robomaster-s1/internal/teleop"
)

var (
	addr       = flag.String("addr", "localhost:8700", "Listen address for the console.")
	wifiDirect = flag.Bool("wifi-direct", true, "Connect via WiFi Direct (robot as AP).")
	appID      = flag.Uint64("appid", 0, "Router mode only: app ID. 0 connects to the first robot found.")
	streamFPS  = flag.Int("fps", 15, "Browser video rate. Encode costs ~23.6 ms/frame, so 30 would burn most of a core.")
	quality    = flag.Int("quality", 70, "JPEG quality for the browser stream.")
	maxChassis = flag.Float64("max-chassis", 0.35, "Chassis deflection clamp, fraction of full stick.")
	maxGimbal  = flag.Float64("max-gimbal", 0.50, "Gimbal deflection clamp, fraction of full stick.")
	deadman    = flag.Duration("deadman", 250*time.Millisecond, "Stop the vehicle after this much producer silence.")
	mock       = flag.Bool("mock", false, "Run with no robot: synthetic video and a sink that discards. For working on the UI.")
	verbose    = flag.Bool("v", false, "Verbose bridge logging.")
)

func main() {
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	gov := safety.New(safety.Config{
		Deadman:    *deadman,
		MaxChassis: *maxChassis,
		MaxGimbal:  *maxGimbal,
		// AllowIntentFire stays false: no model actuates the blaster
		// (DECISIONS.md #7). This console is a human, so it may.
	})

	hub := teleop.NewFrameHub(*quality)
	stop := make(chan struct{})
	go hub.Run(stop, *streamFPS)
	defer close(stop)

	ctx, cancel := signalContext()
	defer cancel()

	var (
		sink     driver.Sink
		statusFn func() teleop.Status
		cleanup  func()
	)

	if *mock {
		log.Warn("mock mode: no robot, synthetic video")
		sink = mockSink{log: log}
		statusFn = func() teleop.Status { return teleop.Status{Connected: false} }
		go mockVideo(ctx, hub)
	} else {
		c, st, clean, err := connectRobot(log)
		if err != nil {
			log.Error("could not reach the robot", "err", err)
			fmt.Fprintln(os.Stderr, "\nIs it powered on, and is Wi-Fi joined to its AP? See docs/M1-RUNBOOK.md.")
			os.Exit(1)
		}
		sink = &robotSink{client: c}
		statusFn = st
		cleanup = clean
		go pumpVideo(c, hub)
	}
	if cleanup != nil {
		defer cleanup()
	}

	srv := teleop.New(teleop.Config{
		Addr: *addr, StreamFPS: *streamFPS, Quality: *quality,
		StatusFn: statusFn, Log: log,
	}, gov, hub)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		driver.Run(ctx, gov, sink, driver.Options{
			OnTick:  srv.ObserveTick,
			OnError: func(err error) { log.Debug("sink error", "err", err) },
		})
	}()

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, c2 := context.WithTimeout(context.Background(), 3*time.Second)
		defer c2()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	fmt.Printf("\n  console:  http://%s\n", *addr)
	fmt.Printf("  clamps:   chassis %.2f · gimbal %.2f · deadman %s\n", *maxChassis, *maxGimbal, *deadman)
	fmt.Printf("  video:    %d fps, quality %d\n\n", *streamFPS, *quality)

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "err", err)
	}

	cancel()
	wg.Wait()
	log.Info("stopped; vehicle sent a final zero")
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}

// ---- real robot ------------------------------------------------------------

func connectRobot(log *slog.Logger) (*robomaster.Client, func() teleop.Status, func(), error) {
	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelDebug
	}
	l := logger.New(level)

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
		return nil, nil, nil, fmt.Errorf("creating client: %w", err)
	}
	if err := c.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("starting client: %w", err)
	}
	if !c.Robot().WaitForDevices(20 * time.Second) {
		_ = c.Stop()
		return nil, nil, nil, errors.New("robot did not report its devices within 20s")
	}

	if err := c.Robot().EnableFunction(robot.FunctionTypeMovementControl, true); err != nil {
		log.Warn("enabling movement control", "err", err)
	}
	if err := c.Robot().EnableFunction(robot.FunctionTypeGunControl, true); err != nil {
		log.Warn("enabling gun control", "err", err)
	}
	if lvl, err := c.Robot().ChassisSpeedLevel(); err == nil {
		_ = c.Robot().SetChassisSpeedLevel(robot.ChassisSpeedLevelSlow)
		log.Info("chassis speed level set to slow", "previous", lvl)
	}

	log.Info("connected", "devices", c.Robot().Devices())

	status := func() teleop.Status {
		pct, ok := safeBattery(c)
		return teleop.Status{Connected: true, Battery: int(pct), HaveBatt: ok}
	}
	return c, status, func() { _ = c.Stop() }, nil
}

// safeBattery guards upstream's BatteryPowerPercent, which dereferences an
// atomic pointer that is nil until the robot pushes its first battery event.
func safeBattery(c *robomaster.Client) (pct uint8, ok bool) {
	defer func() {
		if recover() != nil {
			pct, ok = 0, false
		}
	}()
	return c.Robot().BatteryPowerPercent(), true
}

func pumpVideo(c *robomaster.Client, hub *teleop.FrameHub) {
	_, _ = c.Camera().AddVideoCallback(func(frame *camera.RGB) {
		b := frame.Bounds()
		hub.Submit(frame.Pix, b.Dx(), b.Dy())
	})
}

// robotSink is the only thing in this binary that touches the vehicle, and it
// is only ever called by the driver loop with governor-approved values.
type robotSink struct{ client *robomaster.Client }

func (s *robotSink) Move(cx, cy, gx, gy float64) error {
	chassis := controller.StickPosition{X: cx, Y: cy}
	gimbal := controller.StickPosition{X: gx, Y: gy}
	return s.client.Controller().Move(&chassis, &gimbal, controller.ModeFPV)
}

// Fire is infrared only. TypeBead launches physical gel projectiles and is
// deliberately not reachable from the console (DECISIONS.md #7).
func (s *robotSink) Fire(int) error {
	return s.client.Gun().Fire(gun.TypeInfrared)
}

// ---- mock ------------------------------------------------------------------

type mockSink struct{ log *slog.Logger }

func (mockSink) Move(float64, float64, float64, float64) error { return nil }
func (mockSink) Fire(int) error                                { return nil }

// mockVideo generates frames so the console can be developed and demonstrated
// without a vehicle. A moving bar makes dropped or stale frames obvious by eye.
func mockVideo(ctx context.Context, hub *teleop.FrameHub) {
	const w, h = 1280, 720
	pix := make([]byte, w*h*3)
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	var f int
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f++
			bar := (f * 12) % w
			for y := 0; y < h; y++ {
				row := y * w * 3
				for x := 0; x < w; x++ {
					i := row + x*3
					near := x-bar < 40 && x-bar > -40
					switch {
					case near:
						pix[i], pix[i+1], pix[i+2] = 88, 166, 255
					default:
						v := byte(16 + (x*24)/w + (y*24)/h)
						pix[i], pix[i+1], pix[i+2] = v, v, v+8
					}
				}
			}
			hub.Submit(pix, w, h)
		}
	}
}
