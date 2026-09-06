# dji-robomaster-s1

**Driving a shelf-retired DJI RoboMaster S1 from a browser, with a vision model riding along.**

DJI abandoned the S1. The onboard "AI" was the disappointing part; the
omnidirectional Mecanum chassis was the good part. This moves the intelligence
off the robot and onto a Mac: a full-screen browser console, a safety layer
between the operator and the wheels, and two perception tiers that watch the
robot's camera — a detector drawing boxes at ~10 ms, and a vision-language model
narrating the room in prose every 20 seconds.

**The human drives.** Nothing a model produces actuates anything, and that is a
measured decision rather than a missing feature — see
[docs/BAKEOFF.md](docs/BAKEOFF.md) and `DECISIONS.md` #15. Every drive is
recorded, so the demonstration data exists for the day that changes.

No modification to the robot: it runs stock firmware and is driven by
impersonating the mobile app.

```
 browser — full-screen video, WASD + gamepad
     │  WebSocket: intent up, telemetry down · MJPEG video down
     ▼
 ┌────────────────────────────────────┐
 │  s1teleop              one process │
 │  ────────────────────────────────  │
 │  console            :8700          │
 │  safety governor    ← last hop     │
 │  control loop       20 Hz          │
 │  drive recorder     every drive    │
 └───┬────────────────────────────┬───┘
     │                            │  HTTP
     │  UnityBridge               │  GET  /frame.jpg
     │  app mode, amd64/Rosetta   │  POST /perception
     ▼                            ▼
 ╔═══════════════════╗   ┌────────────────────────────┐
 ║  RoboMaster S1    ║   │  detector    boxes ~10 ms  │
 ║  stock firmware   ║   │  s1narrate   prose / 20 s  │
 ╚═══════════════════╝   └────────────────────────────┘
```

Three structural ideas do most of the work here:

**Everything that touches the robot is one Go process.** DJI's bridge handle is
process-wide, so control and video cannot be separate services — and that is
also why the safety governor lives inside it, on the last hop before the wire
where nothing can route around it (DECISIONS.md #6, #9, #13).

**The browser sends intent, never authority.** Held keys and gamepad axes go to
the governor, which clamps them; the console is never trusted to limit anything.
Closing the tab is treated as a dead producer and the deadman stops the vehicle
— deliberately the same path as a crash or a Wi-Fi drop.

**Perception tiers pull frames over HTTP and post observations back.** No
broker, no shared filesystem — which is why a Python detector and a Go narrator
sit side by side without either knowing the other exists, and why a slow model
can never delay the control loop.

## Status

**A working control system with an observer aboard.** M0–M4 are complete and
verified on hardware: a stock, unmodified S1 driven from a browser with live
video, through a safety layer that stops the vehicle when anything goes quiet,
while a local vision model narrates what it sees and every drive is recorded.

| | |
|---|---|
| Transport | app-mode UnityBridge on **stock firmware 00.06.0518** — no rooting |
| Video | 1280×720 at **30.1 fps**, 0% deficit, **0.24 cores** under Rosetta |
| Under motion | drive motors, strafes and 360° rotation change nothing |
| Link | router mode — jitter **3.3 ms σ**, ~7× better than the robot's own AP |
| Control | 20 Hz rate commands under a 250 ms deadman |
| Observer | `gemma4:e4b` narrating in prose every 20 s, 1.6–3.7 s per caption |
| Detector | yolo11n on MPS, 7–17 ms a frame — drawn and logged, never actuating |
| Recording | every drive, on by default — `logs/drives/<timestamp>/` |
| Tests | **49** (61 with subtests), race-clean |

M4 is done: the perception tiers pull frames over HTTP and post observations
back, with no broker and no shared filesystem (DECISIONS.md #14). Next is M4.5,
an advisory looming highlight in the console — wired to nothing, so that the
heuristic can be calibrated against real driving before anything is allowed to
act on it.

See [docs/STATUS.md](docs/STATUS.md) for where things stand,
[docs/SETUP.md](docs/SETUP.md) for the environment, and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design.

## Running it

```bash
./scripts/install-bridge.sh && ./scripts/build.sh   # once
./scripts/start.sh                                  # console at :8700
```

`start.sh` brings up all three processes — console, observer, detector — and
Ctrl-C stops them together. It skips the observer if Ollama is not answering and
the detector if `uv` is missing, because the console alone is worth running.
Per-process output lands in `logs/run/`.

```bash
./scripts/start.sh -mock   # no robot: synthetic video, a sink that discards
./bin/s1find               # is the robot on the network?
```

Anything passed to `start.sh` goes to `s1teleop`, so `-wifi-direct`,
`-speed-level fast` and the rest work the same way. To run a process on its own
— which is what you want when you are changing one of them — start each by hand:

```bash
./bin/s1teleop                                  # the console and the robot
./bin/s1narrate -v                              # the observer
cd perception/detector && uv run detect.py -v   # the detector
```

## Documentation

| Doc | What's in it |
|---|---|
| [docs/STATUS.md](docs/STATUS.md) | **Start here.** Where things stand, open questions, session log |
| [docs/SETUP.md](docs/SETUP.md) | **The working setup** — machines, network, toolchain, and the traps |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Services, command model, safety rules, measured latency budget |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Every non-obvious choice, with rationale and revisit triggers |
| [docs/HARDWARE.md](docs/HARDWARE.md) | The three transport paths and the firmware situation |
| [docs/M1.md](docs/M1.md) | The latency milestone: what was measured, and what cannot be |
| [docs/M1-RUNBOOK.md](docs/M1-RUNBOOK.md) | Offline field card for working with the robot |
| [docs/M3.md](docs/M3.md) | The browser console: running it, controls, what it measures |
| [docs/M4.md](docs/M4.md) | Perception transport: cadence classes, and why observations are dated |
| [docs/BAKEOFF.md](docs/BAKEOFF.md) | Model comparison on real frames — local and hosted, and what it settled |
| [docs/SPIKE-arm64-bridge.md](docs/SPIKE-arm64-bridge.md) | Why `s1teleop` runs under Rosetta, and what native arm64 would cost |

## Built on

- **[brunoga/robomaster](https://github.com/brunoga/robomaster)** — the Go
  library that speaks the S1's app-mode protocol. Everything here stands on it;
  without it there is no project.
- **[Ultralytics YOLO](https://github.com/ultralytics/ultralytics)** for the
  detector, **[Ollama](https://ollama.com)** for local vision models
  (`gemma4:e4b`, `qwen2.5vl`).
- Runtime and model measurements were first established in a sibling perception
  project and re-verified here on the robot's own frames.

**DJI's UnityBridge library is proprietary and is not distributed in this
repository.** `scripts/install-bridge.sh` copies it out of the
`brunoga/robomaster` module cache into `~/.unitybridge` on your own machine.

## Status and scope

A working, well-instrumented toy — not a product. It drives one specific robot
on one specific desk, and the parts that matter are written down: 20 numbered
decisions with the evidence behind them and the conditions that would reverse
them. Several exist because something surprising happened on a real floor.

## Licence

[MIT](LICENSE), covering the code in this repository. Third-party terms —
including DJI's proprietary UnityBridge, which is **not** distributed here — are
in [NOTICE](NOTICE).

## Safety

This drives a physical vehicle that carries a gel-blaster turret.

- Every command passes through a governor with a deadman timer, deflection
  clamps and an e-stop, enforced on the last hop before the wire — see
  `internal/safety` and `ARCHITECTURE.md` §5.
- The blaster is **infrared only** in this codebase. Bead firing exists in the
  underlying library and is deliberately unreachable from the console.
- No model output actuates anything (`DECISIONS.md` #15).

If you fork this, keep the governor between whatever you build and the wheels.
