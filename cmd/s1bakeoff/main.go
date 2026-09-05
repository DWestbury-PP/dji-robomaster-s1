// Command s1bakeoff scores candidate vision models on a recorded corpus.
//
// Every model sees the same frames, so the comparison is controlled and
// reproducible. It measures what actually decides whether a model is usable on
// a robot: how long it takes, whether its output can be parsed at all, and
// whether the content survives reading.
//
// Structured output is grammar-constrained, so *syntactic* validity is close to
// guaranteed. That is exactly why the interesting failure is semantic — a
// schema-valid wrong answer that downstream code trusts. The report surfaces
// heuristics for that and dumps every answer for a human to read, because no
// automated score substitutes for looking.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	corpus  = flag.String("corpus", "", "Corpus directory from s1capture. Required.")
	models  = flag.String("models", "qwen2.5vl:7b", "Comma-separated Ollama models to score.")
	host    = flag.String("host", "http://localhost:11434", "Ollama endpoint.")
	sample  = flag.Int("sample", 12, "Frames to score per model, spread evenly through the corpus.")
	timeout = flag.Duration("timeout", 180*time.Second, "Per-request timeout.")
	outDir  = flag.String("out", "runs/bakeoff", "Where to write the report and raw answers.")
)

type manifest struct {
	Note   string `json:"note"`
	Frames []struct {
		File   string `json:"file"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"frames"`
}

type result struct {
	Model    string
	Frame    string
	Total    time.Duration
	TTFT     time.Duration // time to first token: what a streaming consumer feels
	Valid    bool
	ParseErr string
	Raw      string
	Scene    Scene
}

func main() {
	flag.Parse()
	if *corpus == "" {
		fmt.Fprintln(os.Stderr, "error: -corpus is required")
		os.Exit(2)
	}

	man, err := loadManifest(*corpus)
	if err != nil {
		fmt.Fprintf(os.Stderr, "corpus: %v\n", err)
		os.Exit(1)
	}
	frames := pick(man, *sample)
	names := strings.Split(*models, ",")

	fmt.Printf("corpus %s — %d frames, scoring %d of them\n", *corpus, len(man.Frames), len(frames))
	fmt.Printf("note: %s\n\n", man.Note)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "out dir: %v\n", err)
		os.Exit(1)
	}

	var all []result
	for _, model := range names {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		fmt.Printf("── %s\n", model)

		for i, f := range frames {
			img, err := os.ReadFile(filepath.Join(*corpus, f))
			if err != nil {
				fmt.Fprintf(os.Stderr, "   read %s: %v\n", f, err)
				continue
			}
			r := score(*host, model, f, img, *timeout)
			all = append(all, r)

			status := "ok "
			if !r.Valid {
				status = "BAD"
			}
			fmt.Printf("   [%2d/%d] %s %s  %6.1fs total  %5.1fs first-token  %s\n",
				i+1, len(frames), status, f,
				r.Total.Seconds(), r.TTFT.Seconds(), truncate(r.Scene.Summary, 44))
		}
		fmt.Println()
	}

	report(all, names, *outDir)
}

// score runs one frame through one model, streaming so we can measure time to
// first token separately from total. On a robot they are different costs: TTFT
// is when a partial answer could start being useful, total is when the belief
// is complete.
func score(host, model, frame string, img []byte, timeout time.Duration) result {
	r := result{Model: model, Frame: frame}

	body, _ := json.Marshal(map[string]any{
		"model":  model,
		"stream": true,
		"format": SceneSchema,
		"messages": []map[string]any{{
			"role":    "user",
			"content": Prompt,
			"images":  []string{base64.StdEncoding.EncodeToString(img)},
		}},
		"options": map[string]any{
			// Temperature 0 is the standard advice for schema adherence, and we
			// want determinism for a comparison anyway.
			"temperature": 0,
			"num_predict": 400,
		},
	})

	client := &http.Client{Timeout: timeout}
	start := time.Now()
	res, err := client.Post(host+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		r.ParseErr = err.Error()
		r.Total = time.Since(start)
		return r
	}
	defer res.Body.Close()

	var sb strings.Builder
	dec := json.NewDecoder(res.Body)
	for {
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := dec.Decode(&chunk); err != nil {
			break
		}
		if chunk.Message.Content != "" && r.TTFT == 0 {
			r.TTFT = time.Since(start)
		}
		sb.WriteString(chunk.Message.Content)
		if chunk.Done {
			break
		}
	}
	r.Total = time.Since(start)
	r.Raw = sb.String()

	if err := json.Unmarshal([]byte(r.Raw), &r.Scene); err != nil {
		r.ParseErr = err.Error()
		return r
	}
	r.Valid = true
	return r
}

func report(all []result, models []string, outDir string) {
	fmt.Println(strings.Repeat("─", 88))
	fmt.Printf("%-22s %5s %8s %8s %9s %8s %8s\n",
		"model", "n", "valid", "p50", "p95", "TTFT p50", "self-ref")
	fmt.Println(strings.Repeat("─", 88))

	for _, m := range models {
		m = strings.TrimSpace(m)
		var rs []result
		for _, r := range all {
			if r.Model == m {
				rs = append(rs, r)
			}
		}
		if len(rs) == 0 {
			continue
		}

		valid, selfRef := 0, 0
		var totals, ttfts []time.Duration
		for _, r := range rs {
			if r.Valid {
				valid++
			}
			totals = append(totals, r.Total)
			ttfts = append(ttfts, r.TTFT)
			// The robot's own barrel is in every frame. A model that reports it
			// as an obstacle would have the reflex layer retreating from
			// itself, so this is the single most consequential hallucination
			// available to it.
			if mentionsSelf(r) {
				selfRef++
			}
		}
		fmt.Printf("%-22s %5d %7d%% %7.1fs %8.1fs %8.1fs %6d/%d\n",
			m, len(rs), valid*100/len(rs),
			pct(totals, 0.50).Seconds(), pct(totals, 0.95).Seconds(),
			pct(ttfts, 0.50).Seconds(), selfRef, len(rs))
	}
	fmt.Println(strings.Repeat("─", 88))
	fmt.Println("valid    = parsed against the schema. Grammar-constrained, so near-100% is expected;")
	fmt.Println("           anything less means the backend struggled with this schema.")
	fmt.Println("self-ref = answers naming the robot's own barrel as an obstacle. Lower is better.")
	fmt.Println("Latency and validity are measured. Whether the answers are TRUE is not — read them.")

	path := filepath.Join(outDir, fmt.Sprintf("bakeoff-%s.json", time.Now().Format("20060102-1504")))
	b, _ := json.MarshalIndent(all, "", "  ")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err == nil {
		fmt.Printf("\nevery answer: %s\n", path)
	}
}

func mentionsSelf(r result) bool {
	needles := []string{"barrel", "gun", "blaster", "turret", "cannon", "nozzle", "orange"}
	hay := strings.ToLower(r.Scene.Summary)
	for _, o := range r.Scene.Obstacles {
		hay += " " + strings.ToLower(o.Object)
	}
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

func loadManifest(dir string) (manifest, error) {
	var m manifest
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	if len(m.Frames) == 0 {
		return m, fmt.Errorf("corpus has no frames")
	}
	return m, err
}

// pick spreads the sample evenly rather than taking the first N, so a corpus
// that starts in one room is not scored entirely on that room.
func pick(m manifest, n int) []string {
	if n <= 0 || n >= len(m.Frames) {
		out := make([]string, len(m.Frames))
		for i, f := range m.Frames {
			out[i] = f.File
		}
		return out
	}
	step := float64(len(m.Frames)) / float64(n)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, m.Frames[int(float64(i)*step)].File)
	}
	return out
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(p*float64(len(s))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
