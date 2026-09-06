# Status

> The living progress log. Read this + [ARCHITECTURE.md](ARCHITECTURE.md)
> to resume work. Rationale: [DECISIONS.md](DECISIONS.md). Transport paths and
> firmware triage: [HARDWARE.md](HARDWARE.md). Environment, network and
> toolchain: [SETUP.md](SETUP.md). Milestone write-ups: [M1.md](M1.md),
> [M3.md](M3.md), [M4.md](M4.md), [BAKEOFF.md](BAKEOFF.md). Investigations:
> [SPIKE-arm64-bridge.md](SPIKE-arm64-bridge.md).

## Where things stand (2026-09-04)

**A working control system with an observer aboard.** A stock, unmodified S1
driven from a browser over the house Wi-Fi, through a safety layer that stops
the vehicle whenever anything goes quiet, while a local vision model narrates
what it sees.

| | |
|---|---|
| Transport | app-mode UnityBridge, **router mode**, stock firmware 00.06.0518 |
| Video | 1280×720 @ 30 fps, jitter **3.3 ms σ**, 0.25 cores under Rosetta |
| Control | 20 Hz rate commands, 250 ms deadman, live gears, e-stop |
| Console | full-bleed cockpit, movable narration, drawer for everything else |
| Observer | `gemma4:e4b` prose caption every 20 s, **1.6–3.7 s** per caption |
| Detector | yolo11n on MPS, **7–17 ms** a frame, boxes drawn and logged |
| Tests | 49 top-level (61 with subtests), race-clean |
| Recording | on by default — `logs/drives/<timestamp>/` |

**Nothing a model produces actuates anything** (DECISIONS.md #15). Motion is
entirely manual.

## M0 result — Path A is closed, Path B is selected

| Vehicle | Active firmware | Staged |
|---|---|---|
| 1 | **00.06.0518** | 00.06.0521 |
| 2 | **00.06.0518** | 00.06.0521 |

00.06.0518 is the version that closes the root exploit, and both units are on
it, so there is no version split to exploit and no known downgrade. **We drive a
stock, unmodified S1 through the app-mode UnityBridge library**
([brunoga/robomaster](https://github.com/brunoga/robomaster)) — HARDWARE.md
Path B, verified this session as maintained, Apple-Silicon-capable via Rosetta,
with the vendored DJI blobs in-repo and a full control + camera surface.

⚠️ **Standing hardware rule: do not let 00.06.0521 install on either vehicle.**
It is staged and one confirmation tap away. Keep the phone off the internet when
using the RoboMaster app.

## What M0 changed in the design

- `s1-link` and `s1-video` **collapsed into one Go process, `s1-driver`** — the
  bridge handle is process-wide, so control and video must share a process
  (DECISIONS.md #9).
- The safety layer is now **written in Go**, still on the last hop (#6).
- Go enters the stack, running as amd64 under Rosetta 2, with a documented exit
  to a Linux host if Rosetta goes away (#10).
- Decision #8 (root one, keep one stock) is **superseded**. Vehicle 2 stays
  pristine as the control and as the future Path C testbed.
- No H.264 parsing needed — the camera module delivers decoded RGB via callback.

## Roadmap

See ARCHITECTURE.md §7.

| Milestone | State |
|---|---|
| M0 — firmware triage, transport choice | ✅ done — Path B |
| M1 — link + latency harness | ✅ **done — GO** (ARCHITECTURE.md §6) |
| arm64 bridge spike | investigated, **deferred** (DECISIONS.md #11) |
| M2 — safety layer (Go) | ✅ done — 25 tests; `-safety-demo` hardware proof still outstanding |
| M3 — browser teleop | ✅ **done — driven on hardware, controls confirmed** |
| M3.5 — router mode | ✅ adopted — jitter ~7× better than the robot's own AP |
| M3.6–3.8 — gears, cockpit UI, movable narration | ✅ done |
| M4 — perception transport | ✅ done (DECISIONS.md #14) |
| M4.1 — model bake-off | ✅ done — [BAKEOFF.md](BAKEOFF.md) |
| M4.2 — fast tier: YOLO boxes | ✅ **done — 7–17 ms, boxes on the console** |
| M4.3 — scene tier: `s1narrate` | ✅ **done — narrating live** |
| M4.4 — the experience log | ✅ **done — recording every drive by default** |
| M4.9 — hide boxes / narration | ✅ done |
| M4.5 — advisory looming highlight | **next** |
| M5 — mobile app | not started |
| ~~intentions, autonomy~~ | **deferred** with conditions (DECISIONS.md #15) |

## M1 complete — GO

Ran against a real vehicle over WiFi Direct, dual-homed (Ethernet for internet,
Wi-Fi for the robot). Numbers in ARCHITECTURE.md §6; raw runs in `runs/`.

- ✅ **Rosetta is a non-issue.** 30.1 fps at 1280×720, zero deficit, 0.24 cores.
  DECISIONS.md #11 stays deferred on evidence.
- ✅ **Motion does not degrade the link.** 31 legs of forward/back, Mecanum
  strafes, 360° rotation, gimbal sweeps and infrared fire — operator-confirmed —
  left every figure inside the stationary range.
- ⚠️ **`Chassis.SetSpeed()` silently does nothing on the S1.** Movement goes
  through `Controller.Move` (virtual stick) at ~20 Hz. Commands are never
  acknowledged, so a nil error proves nothing (DECISIONS.md #12). An earlier
  "motion" run measured a stationary chassis before this was caught.
- ✅ **All 16 devices enumerate**, including Chassis, Gimbal, Camera and WaterGun.
- ⚠️ **Control-plane RTT is not observable through this bridge.** Even with
  `useCache=false` the library answers from its own state in 0.2 ms. Recorded as
  a property of the API, not a measurement.
- ⚠️ **Frame delivery is bursty** — identical moving and still, so it is the
  decoder batching, not the radio. Inside the deadman; a smoothness issue.
  **Corrected 2026-09-05: it was the radio.** Router mode cut jitter ~7× on the
  same decoder, which the batching explanation cannot account for. The
  moving-and-still symmetry was real but did not license the conclusion drawn
  from it (ARCHITECTURE.md §6).
- ⚠️ **Our JPEG encode costs 23.6 ms** of a 33 ms frame budget. Do not encode
  every frame in `s1teleop` — the browser stream defaults to 15 fps for this
  reason.

## M2 — safety layer

`internal/safety` enforces ARCHITECTURE.md §5 on the last hop: deadman,
per-command TTL, deflection clamps, human-only arming with its own expiry,
e-stop, and link loss. Every failure path returns zero and names the rule that
produced it. `internal/driver` runs it at 20 Hz behind a hardware-free `Sink`.

A test caught **lurch-on-release**: commands submitted *during* an e-stop were
latched, so the console's backlog would execute the moment the hold lifted.
`Submit` now refuses while holding.

⬜ **Outstanding:** `s1probe -safety-demo` has never been run. It drives, kills
the producer to time the deadman, then e-stops mid-drive. Needs the vehicle.

## M3 — browser teleop

`s1teleop` serves the console, MJPEG and the command WebSocket from the same
process that holds the bridge (DECISIONS.md #13). Driven on hardware
2026-09-04; the operator confirms the controls feel right.

Three bugs surfaced only by using it, none catchable from software:

1. **Inverted drive and turret pitch.** `controller.StickPosition` negates Y and
   not X, so W drove backward while strafe was fine. Fixed at the adapter —
   `internal/stick.ToVirtual` is now the only place a StickPosition may be built
   from. The same inversion was in `s1probe`'s motion program, so M1's
   "forward" leg actually drove backward; the jitter measurement stands, the
   labels were wrong.
2. **Background tabs stop the robot.** Chrome throttles hidden-tab timers to
   ~1 Hz, so the deadman fires. Correct behaviour, now made legible.
3. A resolution readout that never populated and a command-rate window too short
   to read.

Live video measured **~14.8 fps at 15 fps configured, ~55 KB/frame** — real
imagery compresses about 3× worse than the synthetic test pattern, so roughly
6.6 Mbps.

## Router mode

The S1 now joins the house network rather than serving its own AP.

| | |
|---|---|
| Address | `192.168.1.x` (MAC `60:60:1f:xx:xx:xx` (DJI OUI)) |
| App ID | `<your app id>`, state **paired** |
| Announcement | UDP broadcast on `:45678`, about every 500 ms |

**The DJI app is not needed to bring the robot up.** Measured with no app
running anywhere: the robot joins Wi-Fi on its own and announces itself twice a
second. `bin/s1find` reports what it sees, and answers the question `ping`
cannot — the S1 does not reply to ICMP.

⬜ **Unverified:** whether this survives a power cycle. The test is to power the
robot off and on and run `./bin/s1find` **without opening the app**. If it is
found, the app is never needed again; if not, the credentials do not persist
and that is worth knowing before relying on it.

## The repo is public

**https://github.com/DWestbury-PP/dji-robomaster-s1** — MIT, since 2026-09-05.

Audited before publishing: no secret ever committed, DJI's proprietary blob
never tracked, no recorded frames of the house. Device identifiers (robot MAC,
DJI app ID), specific host addresses and the operator's name were redacted from
the docs; `s1find` tells a reader how to get their own. History deliberately
retains the MAC and app ID in one commit each — both are LAN-local, and the
commit trail is worth more than scrubbing something with no attack surface.

`LICENSE` must stay unmodified MIT or GitHub reports "Other"; third-party terms
live in `NOTICE`.

## Bringing it back up

```bash
./scripts/build.sh
./bin/s1find        # is the robot on the network?
./scripts/start.sh  # all three processes; Ctrl-C stops them together
```

`start.sh` runs the console, the observer and the detector, logging each to
`logs/run/`. It skips the observer if Ollama is not answering and the detector
if `uv` is missing, and refuses to start if something is already listening on
:8700 — a second console fails to bind and dies while the browser still answers
from the first, which looks healthy and is not. Arguments pass through to
`s1teleop`, so `./scripts/start.sh -mock` needs no robot.

To work on one process, start them by hand instead:

```bash
./bin/s1teleop                                 # console at :8700, recording by default
./bin/s1narrate -v                             # the observer
cd perception/detector && uv run detect.py -v  # the detector
```

Order does not matter. The narrator and detector poll and will wait for the
console; the console re-applies session setup when the robot reconnects, so a
battery swap needs no restart.

## What M4 established

The bake-off ([BAKEOFF.md](BAKEOFF.md)) scored candidates on 250 real frames
from this house. Two findings shaped everything after it:

1. **Every model is good at describing and bad at deciding.** gemma4 wrote "the
   path directly ahead is partially obstructed by stacked plastic bins" and in
   the same response returned `clear_path: ahead`, `obstacles: []`. qwen2.5vl
   failed identically in the other direction. That is the constraint tax:
   grammar-constrained decoding guarantees an answer's *shape* and nothing about
   its truth.
2. **Hosted models are not faster, but they are far better at deciding.** Local
   gemma4 beat Haiku, Sonnet and Opus on latency; Opus alone spotted a second
   tracked robot and a person that local models missed entirely — verified by
   eye, not assumed.

Both are now moot for the current design, because #15 stopped asking models to
decide at all. What survives is #16: the scene tier narrates in free prose from
a **local** model, which is what these models were always good at — and dropping
the schema roughly **halved** the latency as well.

## The one-way door — now closed

**M4.4 shipped, and recording is on by default.** Every drive since
2026-09-05 writes `logs/drives/<timestamp>/` with frames, narration, vehicle
state and — the irreplaceable part — the operator's control inputs, logging
both what was *requested* and what the governor *allowed*.

A policy trained on the applied values alone would learn the clamp rather than
the driver, which is why both sides are kept.

Detections join the same log when M4.2 lands; nothing about the format needs to
change for them.

## Open questions

1. ~~**Key discovery.**~~ **Closed 2026-09-04 — no longer needed.** The keys
   this repo requires turned out to be few, and they are all in `cmd/s1teleop`
   and `cmd/s1probe`. A general map of the bridge's key space was never the
   blocker it looked like.
2. ~~**Rosetta decode cost.**~~ **Closed 2026-09-04 — cleared on evidence.**
   30.1 fps, 0% deficit, 0.24 cores under emulation (ARCHITECTURE.md §6). This
   was one of two numbers that could have killed the design; it killed the case
   for the arm64 port instead (DECISIONS.md #11).
3. ~~**Router mode, for range.**~~ **Resolved 2026-09-05 — adopted.** The S1 is
   joined to the house network at `192.168.1.x`. It is **better on every
   measure that matters**, not merely more convenient: jitter fell ~7× and the
   p99 tail 3× against direct mode (ARCHITECTURE.md §6). Dual-homing is gone —
   one network for Redis, Ollama and the robot. Router mode is now the default
   for both binaries; `-wifi-direct` opts back out.
4. **Two vehicles at once.** The bridge handle is process-wide; whether a second
   S1 can be driven from the same host is unknown.
5. **Path C camera.** If we ever replace the intelligent controller, does the FPV
   camera and Wi-Fi go with it? Assumed yes, unverified. Bench question.
6. ~~**foveate M8.**~~ **Moot 2026-09-05.** It was the prerequisite only for the
   Redis-bus version of M4. DECISIONS.md #14 built M4 on an HTTP transport
   inside this repo, so there is nothing to sequence in the foveate session and
   nothing here waits on it.
7. ~~**Battery health.**~~ **Resolved 2026-09-04** — two batteries hold a full
   charge. The short session-1 runtime was an impatient partial charge, not
   degradation. No blocker for M1.

## Session log

**Session 5 (2026-09-05, evening).** Built the fast tier (Python/YOLO, 7–17 ms),
the experience log (on by default, recording both requested and applied
control), and overlay toggles for boxes and narration. Audited and published the
repo under MIT.

Two self-inflicted bugs worth remembering. A `let` declared below its first use
threw at load and killed the whole console script — video, telemetry and all —
while the DOM still looked fine; the browser console found it in one look. And
`git add -A` swept in two byte-identical macOS `name 2.go` duplicates, which Go
compiles as real source, leaving `main` briefly broken *on a public repo*. That
landed on the one commit of the day where the tests were skipped because the
change looked trivial. Tests before every commit, including the trivial ones.

**Session 4 (2026-09-05, afternoon).** Built the perception transport, the
corpus tool and the bake-off harness; scored local and hosted models on 250 real
frames and recorded the result. Narrowed scope to observe-and-log (#15), which
made the scene tier a prose narrator on a local model (#16). Rebuilt the console
as a cockpit (#17), added an honest two-phase cadence readout (#18), and moved
the narration out of the floor's way after driving showed it occluding the
nearest ground (#19). Fixed a power-cycle bug that silently disabled movement
while everything looked healthy, and a HUD that had been claiming a connected
robot for an hour while the vehicle was off.

**Session 3 (2026-09-05).** Operator moved the S1 onto the house network.
Confirmed it announces itself autonomously with no app running, built `s1find`
to prove it, and measured router mode against direct: jitter ~7× better, p99 3×
better. That corrected an over-reached conclusion from session 2 — the
burstiness was the direct-mode radio, not decoder batching. Router mode is now
the default.

**Session 2 (2026-09-04).** M1 hardware bring-up through M3. Connected to a real
vehicle over WiFi Direct; measured §6 and cleared Rosetta on evidence; caught
and corrected the `Chassis.SetSpeed` no-op that had recorded a motion run
against a stationary chassis. Built and merged the safety layer (M2) and the
browser console (M3), revising the architecture to put teleop in the driver
process (#13). Diagnosed the dual-homed same-subnet routing bug that was
dropping API sessions. Fixed inverted drive/turret axes found by driving.
Operator confirms the control experience is good and wants it left as is.

**Session 1 (2026-09-04).** Surveyed the S1 landscape: EP SDK hack (Path A),
app-mode impersonation via UnityBridge (Path B), CAN bus brain transplant
(Path C) — see HARDWARE.md. Confirmed foveate's roadmap already anticipates this
consumer (M10 `intentions`, "schema TBD with the movement system"). Wrote the
initial design set. Operator then powered up a vehicle and read firmware on
both: 00.06.0518 active, 00.06.0521 staged, killing Path A. Verified Path B
against the upstream repo and revised the design around a single Go
`s1-driver`. Ran a 20-minute spike on native-arm64 feasibility — reached a hard
dyld process-platform wall, documented and deferred (#11). Batteries confirmed
healthy. No code.
