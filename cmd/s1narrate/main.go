// Command s1narrate is the observer: it rides along, looks out of the window,
// and says what it sees.
//
// It pulls a frame from a running console on its own schedule, asks a local
// model to describe it in plain prose, and posts the caption back. Nothing
// downstream parses the result and nothing acts on it (DECISIONS.md #15), so
// there is no schema here — free prose is what these models are actually good
// at, and constraining them was producing the worst of their output (#16).
//
// It runs as its own process and pulls rather than being pushed to, so however
// long the model takes it can never delay the control loop or the video.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

var (
	console  = flag.String("console", "http://localhost:8700", "Running s1teleop to pull frames from and post captions to.")
	ollama   = flag.String("ollama", "http://localhost:11434", "Ollama endpoint.")
	model    = flag.String("model", "gemma4:e4b", "Vision model to narrate with.")
	interval = flag.Duration("interval", 20*time.Second, "How often to look. Measured from the start of each cycle, so a slow model does not compound the gap.")
	tier     = flag.String("tier", "scene", "Tier name this narrator reports as.")
	think    = flag.String("think", "off", "Reasoning: 'off', 'on', or 'default'. Off is usually right — reasoning multiplies latency and buys nothing for a caption.")
	timeout  = flag.Duration("timeout", 120*time.Second, "Per-request timeout. Generous: a slow narrator is harmless here.")
	verbose  = flag.Bool("v", false, "Print each caption as it arrives.")
)

// prompt asks for observation, not navigation. The robot's own hardware is
// named explicitly because it is in the bottom centre of every single frame and
// a model that keeps mentioning it makes the narration useless.
const prompt = `You are riding on a small robot about 20cm tall, looking out from just above floor level.

Describe what you see in one or two short sentences: what this space is, and anything
notable in it. Write plainly and concretely, in the present tense.

A barrel and housing are permanently visible at the bottom centre of the frame. They are
part of this robot — never mention them.

You are a passenger, not a navigator. Do not give directions, warnings or advice.`

type frame struct {
	data       []byte
	seq        uint64
	capturedAt time.Time
	w, h       int
}

func main() {
	flag.Parse()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	client := &http.Client{Timeout: *timeout}

	fmt.Printf("narrating from %s\n", *console)
	fmt.Printf("  model     %s (thinking %s)\n", *model, *think)
	fmt.Printf("  every     %s\n\n", *interval)

	for {
		cycleStart := time.Now()

		if err := run(client); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		}

		// Pace from the start of the cycle. If the model took longer than the
		// interval, go again immediately rather than falling further behind.
		wait := *interval - time.Since(cycleStart)
		if wait < 0 {
			wait = 0
		}
		select {
		case <-stop:
			fmt.Println("\nstopped")
			return
		case <-time.After(wait):
		}
	}
}

func run(client *http.Client) error {
	// Announce the start so the console can show the tier working. Without
	// this a caption would simply appear every 20 s with nothing in between,
	// and a slow model would be indistinguishable from a dead one.
	postJSON(client, *console+"/perception/pending", map[string]any{
		"tier": *tier, "model": *model,
		"intervalMs": interval.Milliseconds(),
	})

	f, err := pullFrame(client)
	if err != nil {
		return fmt.Errorf("pulling frame: %w", err)
	}

	start := time.Now()
	text, err := describe(client, f.data)
	latency := time.Since(start)
	if err != nil {
		return fmt.Errorf("describing: %w", err)
	}
	if text == "" {
		return fmt.Errorf("model returned no caption")
	}

	if *verbose {
		fmt.Printf("  %s  %.1fs  %s\n", time.Now().Format("15:04:05"), latency.Seconds(), text)
	}

	// The caption is tied to the frame it describes, not to the moment it
	// arrived — the console renders its age, and by now the robot has moved.
	return postJSON(client, *console+"/perception", map[string]any{
		"tier": *tier, "model": *model,
		"frameSeq":   f.seq,
		"capturedAt": f.capturedAt.Format(time.RFC3339Nano),
		"latencyMs":  float64(latency.Nanoseconds()) / 1e6,
		"width":      f.w, "height": f.h,
		"text": text,
	})
}

func pullFrame(c *http.Client) (frame, error) {
	res, err := c.Get(*console + "/frame.jpg")
	if err != nil {
		return frame{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return frame{}, fmt.Errorf("http %d — is the console connected to a robot?", res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return frame{}, err
	}

	atoi := func(h string) int { n, _ := strconv.Atoi(res.Header.Get(h)); return n }
	seq, _ := strconv.ParseUint(res.Header.Get("X-Frame-Seq"), 10, 64)
	ms, _ := strconv.ParseInt(res.Header.Get("X-Frame-Captured-Unix-Ms"), 10, 64)

	return frame{
		data: data, seq: seq,
		capturedAt: time.UnixMilli(ms),
		w:          atoi("X-Frame-Width"), h: atoi("X-Frame-Height"),
	}, nil
}

func describe(c *http.Client, img []byte) (string, error) {
	req := map[string]any{
		"model":  *model,
		"stream": false,
		"messages": []map[string]any{{
			"role":    "user",
			"content": prompt,
			"images":  []string{base64.StdEncoding.EncodeToString(img)},
		}},
		"options": map[string]any{
			// A little warmth: a caption that varies between similar frames
			// reads as observation, an identical one reads as a stuck field.
			"temperature": 0.4,
			// Generous, because reasoning models draw thinking from the same
			// budget and a tight cap silently truncates the answer to nothing.
			"num_predict": 1200,
		},
	}
	switch *think {
	case "off":
		req["think"] = false
	case "on":
		req["think"] = true
	}

	body, _ := json.Marshal(req)
	res, err := c.Post(*ollama+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("ollama: %s", out.Error)
	}
	return trimCaption(out.Message.Content), nil
}

// trimCaption strips the wrapping some models add around a plain answer.
func trimCaption(s string) string {
	s = string(bytes.TrimSpace([]byte(s)))
	for _, wrap := range []string{`"`, "'"} {
		if len(s) > 1 && string(s[0]) == wrap && string(s[len(s)-1]) == wrap {
			s = s[1 : len(s)-1]
		}
	}
	return s
}

func postJSON(c *http.Client, url string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	res, err := c.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 400))
		return fmt.Errorf("post %s: http %d: %s", url, res.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
