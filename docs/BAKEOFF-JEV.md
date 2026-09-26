# Jev bake-off — 2026-09-21

TypeSafe's **Jev** is a "System One" model: structured decisions instead of
text. You send a `state` blob and a map of `questions`; each is a `choice`
(≤255 options), a `score` (ordered legend) or a `noul` (0–1), and all of them
are evaluated in parallel in one call. It cannot generate strings, and it
takes text only — images must be described to it first.

The claim that made it worth testing is latency. Our scene tier runs 1.6–3.7 s
a caption and #15 rules out anything at that speed touching control. Jev
advertises 70–500 ms, which is the first number anyone has put in front of us
that is the right order of magnitude for a vehicle.

Reproduce: `python3 scripts/jev-probe.py` (stdlib only; reads `JEV_API_KEY`).

**Endpoint.** `POST https://api.typesafe.ai/v1/systemone`, bearer auth,
`jev-1.13.0` (alias `jev-latest`), 64k context, $0.042/MTok in and free out.

## Results

| | |
|---|---|
| Latency, this Mac → their API | **median 319 ms, p90 363 ms, max 480 ms** |
| Cost per decision | **$0.000018** (~430–600 input tokens) |
| Sensor geometry, vs. 5 lines of arithmetic | **6–7/8 against 8/8** |
| VLM prose → structured field | **7/7** |

The two middle rows are the whole story, and they point in opposite
directions. **Jev is worse than arithmetic at the job we imagined for it, and
better than our VLM at a job we had not thought to give it.**

## The finding that matters

**It cannot be trusted with numbers, including numbers it has already been
handed.**

Eight time-of-flight snapshots, scored against the entire competing policy —
`if front < 400: turn toward max(left, right)`:

| wording | score |
|---|---|
| arithmetic | **8/8** |
| Jev, first draft option wording | 3/8 |
| Jev, neutral wording | 6–7/8 |
| Jev, with the arithmetic pre-computed for it | 6/8 |

The first Jev row is a lesson about the API rather than the model. Because
**every option is scored independently against the state**, an option
description is a claim to be matched, not a label. Describing `stop` as "the
situation is unclear or unsafe" makes it match any cluttered scene, and that
one sentence cost half the run. Rewording the five options nearly doubled the
score without touching the model, the state or the question.

The last row is the disqualifying one. There the state reads *"Clearance check
at 400 mm. Directions with room: front, left, right, rear. Most open
direction: front."* — the comparison is done, the answer is in the sentence —
and Jev still returned `strafe_left`.

**The boundary case flips between runs.** `front = 410` against a 400 mm
threshold answered `stop` in one run and `forward` in the next, same input,
same wording. In a control loop the threshold is the only place the decision
is interesting, and that is exactly where it is unstable.

**Why, mechanically.** Independent scoring makes Choice a *categorisation* —
which description does this text match — and not an *optimisation* — which of
these actions is best given a goal. Ticket routing is the former. Picking a
move is the latter. The model is doing what it was built to do; obstacle
avoidance is simply not that shape.

## Where it is genuinely good: reading our own captions

Frame 0208 is the canonical failure in this corpus (BAKEOFF.md:31). gemma4
wrote an accurate sentence —

> the path directly ahead is partially obstructed by a dense, linear
> arrangement of stacked plastic bins

— and returned `clear_path: ahead`, `blocking: false`, `obstacles: []` **in
the same response**. It returned `clear_path: ahead` on 6 of 6 frames.

Handed that same sentence, Jev returns `clear_path: none` at **0.98**
confidence. Across seven captions it scored **7/7**, and confidence fell to
**0.23** on precisely the one that is genuinely ambiguous — frame 0166, where
curtains hang across a doorway and nothing in the prose says whether they are
passable.

That is the split worth having. **The VLM is good at looking and bad at
deciding; Jev is the reverse.** Asking one model to do both is what produced
schema-valid nonsense, and grammar-constrained decoding made it worse rather
than better (#16). Splitting the two costs 300 ms and two hundredths of a
cent.

## Confidence is sharpness, not calibration

The blog says "calibrated probabilities". The documentation is quieter: the
`confidence` field is derived from **how concentrated the probability
distribution is**, no calibration study is offered, and their own caveat is
that it "reflects model certainty, not answer correctness."

Our measurements agree with the documentation rather than the blog:

- On prose it behaves beautifully — 1.00 on the unambiguous captions, 0.23 on
  the genuinely ambiguous one.
- On **garbage input** (`"Sensor snapshot: ?????"`) it returned **0.91**, the
  highest confidence of any sensor call in the run.
- The same fault input scored **0.63 in one run and 0.29 in the next**.

So it is a usable signal for *is this text clean and unambiguous*, and it is
not a safety interlock. A threshold gate of the kind their docs suggest would
not have caught our one dangerous answer.

## The one dangerous answer

`front = 99999` — a plausible overflow or garbage reading from a real sensor —
returned **`forward`**, twice, at 0.63 and then 0.29 confidence. Every other
fault we fed it (all zeros, a negative reading, a stated-faulty sensor,
nonsense text) produced `stop` or `reverse`.

One confidently wrong `forward` on a malformed reading is the entire argument
for keeping range validation upstream of any model, on the ESP32, where a
value outside 40–4000 mm is discarded before anything reasons about it.

## What we do about it

**Jev does not go on the reflex tier.** Three independent reasons, any one of
which is sufficient: 319 ms median against a 250 ms deadman, before the
ESP32's Wi-Fi hop; it loses to five lines of arithmetic; and its answer at the
threshold is not stable between runs.

**Jev is the strongest candidate we have found for the seam #16 left open** —
turning the scene tier's prose into fields something can act on. That seam was
deliberately empty because nothing could fill it reliably. This can.

**#15 is untouched by all of this.** Its claim is that what blocks automated
movement is *geometry, not detection quality*. A model that reads prose
produces no distances, so nothing here earns autonomy. The ToF array remains
the whole unlock, and on that tier the right answer is arithmetic.

## Also learned

- **Options are `criteria`**, an object of name → description, not a list of
  labels. Score levels are an ordered array, numbered from 0, each evaluated
  independently without seeing its neighbours.
- **Batching is strongly preferred** — the docs claim one call carrying every
  question is 12.2× cheaper and 10× faster than separate calls.
- **The context paragraph does most of the work.** With no description of the
  vehicle, Jev answered `forward` to all six opening scenarios including a
  dead end, at a flat ~0.74 confidence. Four sentences of vehicle context
  changed both the answers and the confidences. That is a maintenance
  liability worth naming: if that paragraph drifts from the hardware, nothing
  fails loudly.
- **English only** for good accuracy, and a 32k budget for state plus the
  longest single question.
