# Status

> The living progress log. Read this + [ARCHITECTURE.md](ARCHITECTURE.md)
> to resume work. Rationale: [DECISIONS.md](DECISIONS.md). Transport paths and
> firmware triage: [HARDWARE.md](HARDWARE.md). Environment, network and
> toolchain: [SETUP.md](SETUP.md). Current milestone: [M1.md](M1.md).
> Investigations:
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
- ⚠️ **Our JPEG encode costs 23.6 ms** of a 33 ms frame budget. Do not encode
  every frame in `s1-driver`.

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

1. **Key discovery.** The bridge is a reverse-engineered key/value + event API;
   upstream's README says the remaining work is learning what each key does. M1
   should produce a map of the keys we actually need.
2. **Rosetta decode cost.** Video decode happens inside the emulated blob, not
   on VideoToolbox. Measure in M1; it is one of two numbers that could kill the
   design (ARCHITECTURE.md §6).
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
6. **foveate M8.** Multi-camera + crash recovery is the prerequisite for M4 here.
   Sequence it in the foveate session, not this one.
7. ~~**Battery health.**~~ **Resolved 2026-09-04** — two batteries hold a full
   charge. The short session-1 runtime was an impatient partial charge, not
   degradation. No blocker for M1.

## Session log

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
