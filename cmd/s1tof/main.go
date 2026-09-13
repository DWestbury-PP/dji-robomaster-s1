// Command s1tof asks the robot whether a DJI time-of-flight distance sensor is
// attached.
//
// Why this is its own command rather than a flag on s1teleop: reading an
// undecoded key is not free. The reply is dispatched through
// result.NewFromJSON, which calls key.ResultValue() — and that *panics* for a
// key carrying no decoded type, which every TOF key does. A panic here would
// take a driving vehicle down with it, so the probe gets a throwaway process.
//
// Two things make it safe anyway:
//
//   - The callback passed to GetKeyValue is nil. In notifyCallbacks the decode
//     sits inside `if c != nil`, so a nil callback means the reply is never
//     decoded and the panic never happens.
//   - The raw bytes are still visible, because eventCallback traces "data"
//     before it dispatches anything. That needs a logger at LevelTrace, which
//     is below Debug — s1teleop's -v is not low enough.
//
// So: ask with no callback, and read the answer out of the trace log.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/brunoga/robomaster"
	"github.com/brunoga/robomaster/support/logger"
	"github.com/brunoga/robomaster/unitybridge/unity/key"
)

var (
	appID = flag.Uint64("appid", 0, "Only connect to a robot announcing this app ID.")
	wait  = flag.Duration("wait", 4*time.Second, "How long to wait for replies after asking.")
)

// probes are read-only keys that describe the TOF subsystem. Nothing here
// writes, so none of it can hand DJI's JSON parser something it will abort on.
var probes = []*key.Key{
	// The bolt-on distance sensor accessory.
	key.KeyRobomasterTOFConnection,
	key.KeyRobomasterTOFOnlineModules,
	key.KeyRobomasterTOFFirmwareVersion1,
	key.KeyRobomasterTOFFirmwareVersion2,

	// The Sensor Adapter: DJI's breakout with analog and digital IO for
	// third-party sensors. If this answers, a distance sensor can be read over
	// the bridge we already speak — no second radio, no second battery.
	key.KeyRobomasterSensorAdapterConnection,
	key.KeyRobomasterSensorAdapterOnlineModules,
	key.KeyRobomasterSensorAdapterFirmwareVersion1,

	// The Servo subsystem, same shape, worth asking while we are here.
	key.KeyRobomasterServoConnection,
	key.KeyRobomasterServoOnlineModules,

	// Whether the expansion bus itself reports a firmware version at all.
	key.KeyRobomasterSystemCANFirmwareVersion,

	// The onboard vision system, which is the other candidate for "does this
	// robot already know something is in front of it?".
	key.KeyVisionFirmwareVersion,
	key.KeyVisionDetectionEnable,
	key.KeyVisionDebugRect,
	key.KeyVisionMarkerRunningStatus,
	key.KeyVisionTrackingRunningStatus,
	key.KeyVisionHumanDetectionRunningStatus,
	key.KeyVisionAimbotRunningStatus,
	key.KeyVisionARParameters,
}

func main() {
	flag.Parse()

	// Below Debug: Trace is LevelDebug-1, and the raw event data is only ever
	// traced, never logged at a higher level. logger.New writes to stdout, so
	// the caller redirects it — and note it truncates any attribute past 100
	// characters, which would clip a long reply.
	l := logger.New(logger.LevelTrace)

	c, err := robomaster.New(l, *appID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating client: %v\n", err)
		os.Exit(1)
	}
	if err := c.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "starting client: %v\n\nIs the robot powered on and on the network?\n", err)
		os.Exit(1)
	}
	defer c.Stop()

	if !c.Robot().WaitForDevices(20 * time.Second) {
		fmt.Fprintln(os.Stderr, "robot did not report its devices")
		os.Exit(1)
	}
	fmt.Println("connected:", c.Robot().Devices())
	fmt.Println()

	ub := c.Robot().UB()
	for _, k := range probes {
		// nil callback on purpose: it is what stops the reply being decoded.
		if err := ub.GetKeyValue(k, nil); err != nil {
			fmt.Printf("  %-40s not asked: %v\n", shortName(k), err)
			continue
		}
		fmt.Printf("  %-40s asked\n", shortName(k))
		time.Sleep(300 * time.Millisecond)
	}

	fmt.Printf("\nwaiting %s for replies...\n", *wait)
	time.Sleep(*wait)
	fmt.Println("\ndone — replies are in the trace above")
}

func shortName(k *key.Key) string {
	return strings.TrimPrefix(fmt.Sprintf("%s", k), "KeyRobomaster")
}
