package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic API models are scored through the same harness as local ones, on
// the same frames, so the local-vs-hosted question is answered by measurement
// rather than argument.

// perMTok is input/output pricing in dollars per million tokens, so the report
// can put a number on what a scene tier would actually cost to run.
var perMTok = map[string][2]float64{
	"claude-opus-5":    {5.00, 25.00},
	"claude-sonnet-5":  {2.00, 10.00},
	"claude-haiku-4-5": {1.00, 5.00},
}

func isAnthropic(model string) bool { return strings.HasPrefix(model, "claude-") }

// loadDotEnv reads KEY=VALUE lines into the environment without overwriting
// anything already set. The file is gitignored; nothing here ever logs a value.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}

// thinkingFor applies the per-model reasoning policy.
//
// The three current models differ, and the differences matter here because
// reasoning is where the latency goes:
//
//   - Haiku 4.5 does not reason unless asked (it still takes the older
//     budget_tokens opt-in), so "off" is simply the default.
//   - Sonnet 5 accepts an explicit disable.
//   - Opus 5 reasons by default. Disabling it is accepted at effort high or
//     below, but carries documented failure modes — it can write a tool call
//     into visible text, or leak thinking tags. Lowering effort is the
//     recommended way to make it cheap and fast, so that is what "off" does.
func thinkingFor(model, mode string) (anthropic.ThinkingConfigParamUnion, anthropic.OutputConfigEffort) {
	var none anthropic.ThinkingConfigParamUnion

	if mode != "off" {
		return none, "" // leave the model on its own default
	}

	switch {
	case strings.HasPrefix(model, "claude-haiku"):
		return none, "" // already off by default
	case strings.HasPrefix(model, "claude-opus"):
		return none, anthropic.OutputConfigEffortLow
	default:
		return anthropic.ThinkingConfigParamUnion{
			OfDisabled: &anthropic.ThinkingConfigDisabledParam{},
		}, ""
	}
}

func scoreAnthropic(model, frame string, img []byte, timeout time.Duration, mode string) result {
	r := result{Model: model, Frame: frame}

	client := anthropic.NewClient(option.WithRequestTimeout(timeout))

	thinking, effort := thinkingFor(model, mode)

	out := anthropic.OutputConfigParam{
		// Grammar-constrained output. It guarantees the answer's shape and
		// nothing about its truth — the constraint tax applies here exactly as
		// it does locally (docs/BAKEOFF.md).
		Format: anthropic.JSONOutputFormatParam{Schema: SceneSchema},
	}
	if effort != "" {
		out.Effort = effort
	}

	params := anthropic.MessageNewParams{
		Model:        anthropic.Model(model),
		MaxTokens:    2000,
		OutputConfig: out,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewImageBlockBase64("image/jpeg", base64.StdEncoding.EncodeToString(img)),
				anthropic.NewTextBlock(Prompt),
			),
		},
	}
	if thinking.OfDisabled != nil || thinking.OfAdaptive != nil || thinking.OfEnabled != nil {
		params.Thinking = thinking
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	stream := client.Messages.NewStreaming(ctx, params)

	msg := anthropic.Message{}
	var text strings.Builder
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			r.ParseErr = "accumulate: " + err.Error()
			break
		}
		if d, ok := ev.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			switch delta := d.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				if r.TTFT == 0 {
					r.TTFT = time.Since(start)
				}
				text.WriteString(delta.Text)
			case anthropic.ThinkingDelta:
				r.ThinkChars += len(delta.Thinking)
				r.ThinkMs = float64(time.Since(start).Nanoseconds()) / 1e6
			}
		}
	}
	r.Total = time.Since(start)

	if err := stream.Err(); err != nil {
		r.ParseErr = err.Error()
		return r
	}

	// A refusal is an HTTP 200 with no usable content — check before reading.
	if msg.StopReason == anthropic.StopReasonRefusal {
		r.ParseErr = "refused: " + string(msg.StopDetails.Category)
		return r
	}
	r.Truncated = msg.StopReason == anthropic.StopReasonMaxTokens

	r.InputTokens = msg.Usage.InputTokens
	r.OutputTokens = msg.Usage.OutputTokens
	if p, ok := perMTok[model]; ok {
		r.CostUSD = float64(r.InputTokens)/1e6*p[0] + float64(r.OutputTokens)/1e6*p[1]
	}

	r.Raw = strings.TrimSpace(text.String())
	if r.Raw == "" {
		r.ParseErr = "no answer content"
		if r.Truncated {
			r.ParseErr = "token budget exhausted before any answer"
		}
		return r
	}
	if err := json.Unmarshal([]byte(r.Raw), &r.Scene); err != nil {
		r.ParseErr = fmt.Sprintf("%v", err)
		return r
	}
	r.Valid = true
	return r
}
