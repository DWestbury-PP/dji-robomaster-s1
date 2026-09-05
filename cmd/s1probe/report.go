package main

import (
	"fmt"
	"strings"

	"github.com/brunoga/robomaster/module/chassis"

	"github.com/DWestbury-PP/dji-robomaster-s1/internal/probe"
)

// chassisMode is the mode used for the opt-in chassis nudge. YawFollow is the
// ordinary driving mode.
const chassisMode = chassis.ModeYawFollow

// nominalFPS is what the S1 is expected to stream. Measured FPS well below this
// with CPU pinned is the signature of decode losing to emulation.
const nominalFPS = 30.0

func printReport(r *probe.Report) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 78))
	fmt.Println("M1 — link and latency report")
	fmt.Println(strings.Repeat("─", 78))

	rosetta := "native"
	if r.Host.UnderRosetta {
		rosetta = "UNDER ROSETTA (amd64 on Apple Silicon)"
	}
	fmt.Printf("host        %s/%s  %s  %s\n", r.Host.GOOS, r.Host.GOARCH, r.Host.GoVersion, rosetta)
	fmt.Printf("wifi mode   %s\n", r.WiFiMode)
	if r.ConnectMs > 0 {
		fmt.Printf("connect     %.0f ms\n", r.ConnectMs)
	}

	if r.Video.Frames > 0 {
		fmt.Println()
		fmt.Printf("video       %dx%d, %d frames in %.1fs = %.1f fps (%.0f%% under nominal %.0f)\n",
			r.Video.Width, r.Video.Height, r.Video.Frames, r.Video.DurationSec,
			r.Video.FPS, r.Video.FPSDeficit, nominalFPS)
		fmt.Printf("first frame %.0f ms after subscribe\n", r.FirstFrame)
	}

	if len(r.Series) > 0 {
		fmt.Println()
		fmt.Printf("%-24s %5s %8s %8s %8s %8s %8s\n", "series", "n", "min", "p50", "p95", "p99", "jitter")
		for _, s := range r.Series {
			if s.N == 0 {
				fmt.Printf("%-24s %5d  (no samples)\n", s.Name, s.N)
				continue
			}
			fmt.Printf("%-24s %5d %7.1fms %7.1fms %7.1fms %7.1fms %7.1fms\n",
				s.Name, s.N, s.MinMs, s.P50Ms, s.P95Ms, s.P99Ms, s.StdDevMs)
		}
	}

	fmt.Println()
	fmt.Printf("cpu         %.1fs user + %.1fs sys over %.1fs wall = %.2f cores busy\n",
		r.CPU.UserSec, r.CPU.SystemSec, r.CPU.WallSec, r.CPU.CoresBusy)

	if len(r.Notes) > 0 {
		fmt.Println()
		fmt.Println("notes")
		for _, n := range r.Notes {
			fmt.Printf("  · %s\n", n)
		}
	}

	if r.Incomplete {
		fmt.Println()
		fmt.Println("⚠️  INCOMPLETE — the run did not finish all phases.")
	}
	fmt.Println(strings.Repeat("─", 78))
}
