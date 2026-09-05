# dji-robomaster-s1

**A DIY control and autonomy stack for two shelf-retired DJI RoboMaster S1s.**

DJI abandoned the S1. The onboard "AI" was the disappointing part, the
omnidirectional Mecanum chassis was the good part, and the vehicle is a
perfectly good body looking for a better brain. This repo is that brain's
nervous system: a browser teleop console, a safety layer, and a video path
that feeds [foveate](../foveate) — the tiered perception stack — so a
vision-language model can eventually drive.

```
 browser (WASD / arrows / gamepad)          foveate (peer repo, shared bus)
        │                                   ┌───────────────────────────────┐
        │  WebSocket: intent up,            │  motion │ YOLO │ VLM │ fusion │
        │  telemetry down · MJPEG video     └────▲──────────────────┬───────┘
        ▼                                        │ frames           │ fusion
 ┌──────────────────────────────┐ ───────────────┘                  │
 │  s1teleop  (one Go process)  │                                   ▼
 │  ──────────────────────────  │                            ┌──────────────┐
 │  console  :8700              │ ◄──────── intentions ──────│ intent loop  │
 │  safety governor  ← last hop │                            │   (later)    │
 │  control loop     20 Hz      │                            └──────────────┘
 └──────────────┬───────────────┘
      UnityBridge (app mode, amd64 under Rosetta)
                ▼
         ╔═════════════╗
         ║ RoboMaster  ║  stock, unrooted, fw 00.06.0518
         ║     S1      ║
         ╚═════════════╝
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

**`frames` is published in foveate's own schema**, so the robot's camera will
enter the existing perception pipeline as just another camera. No perception
code is duplicated or forked.

## Status

**A working control system.** M0–M3 are complete and verified on hardware: a
stock, unmodified S1 driven from a browser with live video, through a safety
layer that stops the vehicle when anything goes quiet.

| | |
|---|---|
| Transport | app-mode UnityBridge on **stock firmware 00.06.0518** — no rooting |
| Video | 1280×720 at **30.1 fps**, 0% deficit, **0.24 cores** under Rosetta |
| Under motion | drive motors, strafes and 360° rotation change nothing |
| Link | router mode — jitter **3.3 ms σ**, ~7× better than the robot's own AP |
| Control | 20 Hz rate commands under a 250 ms deadman |
| Tests | **36** (48 with subtests), race-clean |

Next is M4 — publishing `frames` onto foveate's bus so YOLO and the VLM can
narrate the robot's view. See [docs/STATUS.md](docs/STATUS.md) for where things
stand, [docs/SETUP.md](docs/SETUP.md) for the environment, and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design.

```bash
./scripts/install-bridge.sh && ./scripts/build.sh
./bin/s1find              # where is the robot?
./bin/s1teleop            # console at http://localhost:8700
./bin/s1teleop -mock      # no robot needed
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
| [docs/SPIKE-arm64-bridge.md](docs/SPIKE-arm64-bridge.md) | Why `s1-driver` runs under Rosetta, and what native arm64 would cost |
