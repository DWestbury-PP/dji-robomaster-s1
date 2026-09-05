# Model bake-off — 2026-09-05

Candidate scene-tier models scored on `corpus/drive-01`: 250 frames, 124 s of
real driving through a house. Every model saw the same frames.

Reproduce: `./bin/s1bakeoff -corpus corpus/drive-01 -models "…" -think off`

## Results

| Model | valid | p50 | TTFT p50 | notes |
|---|---|---|---|---|
| **gemma4:e4b** (reasoning off) | **100%** | **3.4 s** | **1.1 s** | fastest usable |
| gemma4:e4b (reasoning on) | 100% | 17.1 s | 13.8 s | reasoning is 4× the cost |
| qwen2.5vl:7b | 100% | 7.8 s | 4.8 s | the incumbent from foveate |
| qwen3-vl:8b | 0–66% | 52.4 s | — | reasons regardless; truncates |
| qwen3-vl:30b-a3b | 0% | — | — | reasons regardless |

**The Qwen3-VL family is unusable here**, and not on quality: both sizes ignore
`think: false` in Ollama 0.33.2, reason anyway, and exhaust the token budget
before emitting an answer. A 30B-A3B MoE at ~6 s would otherwise have been the
most interesting candidate on the list. Worth retrying when Ollama's handling of
their reasoning improves.

**Reasoning is not worth its cost here.** gemma4 with reasoning on is 5× slower
and no more accurate on the fields we care about.

## The finding that matters

Every model is good at **describing** and bad at **deciding**.

gemma4 on frame 0208 wrote *"the path directly ahead is partially obstructed by
a dense, linear arrangement of stacked plastic bins"* — correct — and in the
same response returned `clear_path: ahead`, `blocking: false`, `obstacles: []`.
It returned `clear_path: ahead` on **6 of 6** frames and `confidence: high` on
all six.

qwen2.5vl fails identically in the opposite direction: `clear_path: none` on
frames with wide open floor, and once `hazards: ["near","blocking"]` — enum
words from a neighbouring field, entirely schema-valid, entirely meaningless.

One is uselessly optimistic, the other uselessly cautious. Neither is navigable,
and shopping for a better model does not fix it: we are asking a language model
for a geometric judgement.

This is the **constraint tax** the literature warns about, observed on our own
frames: grammar-constrained decoding guarantees the *shape* of an answer and
guarantees nothing about its truth, which turns a visible failure into a
schema-valid wrong decision that downstream code trusts.

## Round 2: hosted models (2026-09-05)

Same corpus, same frames, same schema, through the Anthropic API.

| Model | valid | p50 | TTFT | cost/call | cost/hr @0.1 Hz |
|---|---|---|---|---|---|
| **gemma4:e4b** (local) | 100% | **3.5 s** | **1.0 s** | — | **$0** |
| claude-haiku-4-5 | 100% | 5.4 s | 1.6 s | $0.0027 | $0.98 |
| claude-sonnet-5 | 100% | 5.5 s | 1.5 s | $0.0076 | $2.75 |
| claude-opus-5 | 100% | 7.6 s | 2.1 s | $0.0197 | $7.08 |

**Hosted is not faster.** The local 4B model beat every hosted model on latency;
a network round trip plus a larger model does not win on speed. Anyone reaching
for an API to reduce latency here would be reaching for the wrong lever.

**Hosted is decisively better at deciding**, which is the thing that was broken.

On frame 0166 — an aquarium on a stand, curtains across a doorway, open floor:

| Model | obstacles returned |
|---|---|
| gemma4:e4b | `[]` — saw nothing |
| claude-haiku-4-5 | aquarium (left, far), curtains (ahead, far) |
| claude-sonnet-5 | + cables, wall/door edge |
| claude-opus-5 | + low shelf edge, power cable on floor; and the only model to report `confidence: medium` / `lighting: dim` where others claimed `high` / `good` |

On the cluttered frame 0208, **only Sonnet 5 returned `clear_path: none`**, with
three obstacles marked blocking, consistent with its own summary. Every local
model contradicted itself here.

**The extra detail is accurate, not hallucinated.** Verified against the frame
by eye: Opus correctly identified a second tracked robot on the floor and a
person's leg beside the chair — both entirely missed by the local models — and
hedged the ambiguous one as *"chair leg / person's leg"*. Its six obstacles all
check out.

**What has not changed:** every model here still takes 5–8 s. None can sit in a
control loop, so the geometry-owns-clearance split below stands regardless of
which model wins.

### Cost note

Prices are for a 1280×720 frame (~1,230 image tokens). Downscaling to 640×360
cuts image tokens roughly 4× and is worth testing — our frames carry heavy
motion blur, so the extra resolution may be buying nothing.

## What we do about it

Split responsibility by what each tier is actually good at.

| Question | Owner | Why |
|---|---|---|
| Is the way clear? How close? | **fast tier** — YOLO + ground-plane geometry | a spatial question deserves a spatial answer; it also feeds the veto clamp |
| What *is* this place? What should I be careful of? | **scene tier** — a hosted model | needs world knowledge and judgement; local 4B describes but cannot decide |

For the scene tier, **claude-sonnet-5 is the recommendation** at $2.75/hr of
continuous driving: it was the only model to call a blocked path blocked, and
its obstacle lists are specific and internally consistent. Opus 5 is richer
still — it alone spotted the second robot and a person — and costs 2.6× more for
2 s more latency; worth it when analysis matters more than pace. Haiku 4.5 at
$0.98/hr is a real option if the tier runs constantly. gemma4:e4b stays useful
as the offline fallback when there is no network.

This is foveate's tier split — fast tier authoritative for *where and how many*,
slow tier for *what and in what state* — reached independently from our own
measurements rather than inherited.

It also settles a design question already decided on other grounds: the reflex
veto takes its input from **geometry, never from the language model**.

## Consequences for the schema

`SceneSchema` currently asks the model to decide (`clear_path`, `blocking`,
`proximity`). Those fields should go, or be demoted to advisory and never
consumed by control. What is worth keeping is what it does well: a prose
summary, named objects, hazards in words, people and pet counts, and a lighting
assessment — it correctly called "backlit" on every frame that was.

## Also learned

- **The robot's own gel blaster barrel is in every frame**, mounted beside the
  camera. Naming it in the prompt was enough: 0 self-references across all runs.
- **Reasoning output is a separate field.** Ollama streams `message.thinking`
  apart from `message.content`; the first version of this harness dropped it and
  made two capable models look broken. Token budget is shared between them, so a
  tight `num_predict` silently truncates the answer to nothing.
