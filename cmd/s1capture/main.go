// Command s1capture builds a frame corpus by pulling from a running s1teleop
// while you drive.
//
// The corpus is the point: every candidate model gets scored on the *same*
// frames, so the comparison is controlled and reproducible weeks later. Live
// frames would be faster to get but no two models would ever see the same
// scene, which makes the numbers unfalsifiable.
//
// It pulls from /frame.jpg rather than the stream, and skips frames whose
// sequence number has not advanced, so a corpus never contains duplicates.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

var (
	server   = flag.String("server", "http://localhost:8700", "Running s1teleop to pull from.")
	out      = flag.String("out", "", "Corpus directory. Required.")
	interval = flag.Duration("interval", 500*time.Millisecond, "How often to pull. Frames repeat if the robot is still, and duplicates are skipped.")
	count    = flag.Int("count", 200, "Stop after this many distinct frames. 0 means run until Ctrl-C.")
	note     = flag.String("note", "", "Free-text note stored in the manifest — where this was shot, what is in it.")
)

// Entry is one corpus frame. The capture timestamp travels with it so a scored
// observation can still be dated long after the drive.
type Entry struct {
	File       string `json:"file"`
	Seq        uint64 `json:"seq"`
	CapturedMs int64  `json:"capturedUnixMs"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Bytes      int    `json:"bytes"`
}

type Manifest struct {
	CreatedAt time.Time `json:"createdAt"`
	Server    string    `json:"server"`
	Note      string    `json:"note,omitempty"`
	Frames    []Entry   `json:"frames"`
}

func main() {
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "error: -out is required")
		flag.Usage()
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create %s: %v\n", *out, err)
		os.Exit(1)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	man := Manifest{CreatedAt: time.Now(), Server: *server, Note: *note}
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	var lastSeq uint64
	var skipped int

	fmt.Printf("capturing to %s — drive the robot around; Ctrl-C to finish\n\n", *out)

loop:
	for {
		select {
		case <-stop:
			break loop
		case <-ticker.C:
		}

		e, data, err := pull(client, *server)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  pull failed: %v\n", err)
			continue
		}
		// The robot sitting still produces the same frame repeatedly. A corpus
		// full of one scene would flatter every model equally and tell us
		// nothing.
		if e.Seq == lastSeq {
			skipped++
			continue
		}
		lastSeq = e.Seq

		e.File = fmt.Sprintf("%04d.jpg", len(man.Frames))
		if err := os.WriteFile(filepath.Join(*out, e.File), data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "  write failed: %v\n", err)
			continue
		}
		man.Frames = append(man.Frames, e)

		fmt.Printf("\r  %d frames (%d duplicates skipped)", len(man.Frames), skipped)

		if *count > 0 && len(man.Frames) >= *count {
			break
		}
	}

	fmt.Printf("\n\n")
	if len(man.Frames) == 0 {
		fmt.Fprintln(os.Stderr, "no frames captured — is s1teleop running and connected?")
		os.Exit(1)
	}

	b, _ := json.MarshalIndent(man, "", "  ")
	mp := filepath.Join(*out, "manifest.json")
	if err := os.WriteFile(mp, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "writing manifest: %v\n", err)
		os.Exit(1)
	}

	span := time.Duration(0)
	if n := len(man.Frames); n > 1 {
		span = time.Duration(man.Frames[n-1].CapturedMs-man.Frames[0].CapturedMs) * time.Millisecond
	}
	fmt.Printf("  %d frames over %s\n  %s\n", len(man.Frames), span.Round(time.Second), mp)
}

func pull(c *http.Client, server string) (Entry, []byte, error) {
	res, err := c.Get(server + "/frame.jpg")
	if err != nil {
		return Entry{}, nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Entry{}, nil, fmt.Errorf("http %d", res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return Entry{}, nil, err
	}

	atoi := func(h string) int { n, _ := strconv.Atoi(res.Header.Get(h)); return n }
	seq, _ := strconv.ParseUint(res.Header.Get("X-Frame-Seq"), 10, 64)
	capMs, _ := strconv.ParseInt(res.Header.Get("X-Frame-Captured-Unix-Ms"), 10, 64)

	return Entry{
		Seq: seq, CapturedMs: capMs,
		Width: atoi("X-Frame-Width"), Height: atoi("X-Frame-Height"),
		Bytes: len(data),
	}, data, nil
}
