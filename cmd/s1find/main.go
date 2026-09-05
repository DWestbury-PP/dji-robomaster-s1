// Command s1find listens for the RoboMaster's presence broadcast on the local
// network and reports what it is announcing.
//
// In router mode the robot announces itself by UDP broadcast on :45678 rather
// than being dialled at a fixed address. That makes this the diagnostic for
// "is the robot actually on the network?" — and, because the broadcast carries
// the pairing flag and app ID, for "does it still think it belongs to the DJI
// app?" too.
//
// It answers a question `ping` cannot: the S1 does not reply to ICMP, so
// silence there proves nothing either way.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/brunoga/robomaster/support/finder"
	"github.com/brunoga/robomaster/support/logger"
)

// broadcastPort is where the robot announces itself. Fixed by DJI.
const broadcastPort = ":45678"

var (
	timeout = flag.Duration("timeout", 15*time.Second, "How long to wait for a broadcast.")
	watch   = flag.Bool("watch", false, "Keep listening and report every broadcast packet, with the gap between them. Useful for confirming the robot is still on the network.")
	appID   = flag.Uint64("appid", 0, "Only report robots with this app ID. 0 reports any, paired or not.")
)

func main() {
	flag.Parse()

	f := finder.New(*appID, logger.New(slog.LevelError))

	if !*watch {
		fmt.Printf("listening for a RoboMaster broadcast (up to %s)...\n\n", *timeout)
		b, err := f.Find(*timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "no robot announced itself within %s: %v\n\n", *timeout, err)
			fmt.Fprintln(os.Stderr, "That means one of:")
			fmt.Fprintln(os.Stderr, "  · the robot is powered off, or still booting")
			fmt.Fprintln(os.Stderr, "  · it has not joined the network (router mode not configured, or")
			fmt.Fprintln(os.Stderr, "    credentials lost — reconfigure via the DJI app)")
			fmt.Fprintln(os.Stderr, "  · this host is on a different network or VLAN from the robot")
			fmt.Fprintln(os.Stderr, "  · the access point blocks client-to-client traffic (AP isolation)")
			os.Exit(1)
		}
		report(b, 0)
		return
	}

	// The library's Finder deduplicates by robot — it reports each one once,
	// which is right for connecting but useless for "is it still there?". So
	// watch mode reads the broadcast socket directly and reports every packet.
	conn, err := net.ListenPacket("udp4", broadcastPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not listen on %s: %v\n", broadcastPort, err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Printf("watching %s for broadcasts; Ctrl-C to stop\n\n", broadcastPort)

	buf := make([]byte, 1024)
	var last time.Time
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read: %v\n", err)
			return
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		b, err := finder.ParseBroadcast(data)
		if err != nil {
			continue // not a RoboMaster announcement
		}
		if *appID != 0 && b.AppId() != *appID {
			continue
		}

		gap := time.Duration(0)
		if !last.IsZero() {
			gap = time.Since(last)
		}
		last = time.Now()
		report(b, gap)
	}
}

func report(b *finder.Broadcast, gap time.Duration) {
	state := "paired"
	if b.IsPairing() {
		state = "PAIRING — waiting to be claimed by an app"
	}

	fmt.Printf("%s  robot found\n", time.Now().Format("15:04:05"))
	fmt.Printf("  ip       %s\n", b.SourceIp())
	fmt.Printf("  mac      %s\n", b.SourceMac())
	fmt.Printf("  app id   %d\n", b.AppId())
	fmt.Printf("  state    %s\n", state)
	if gap > 0 {
		fmt.Printf("  gap      %s since the previous broadcast\n", gap.Round(time.Millisecond))
	}
	fmt.Println()
}
