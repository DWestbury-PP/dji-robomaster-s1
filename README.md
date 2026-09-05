# dji-robomaster-s1

**A DIY control and autonomy stack for two shelf-retired DJI RoboMaster S1s.**

DJI abandoned the S1. The onboard "AI" was the disappointing part, the
omnidirectional Mecanum chassis was the good part, and the vehicle is a
perfectly good body looking for a better brain. This repo is that brain's
nervous system: a browser teleop console, a safety layer, and a video path
that feeds [foveate](../foveate) — the tiered perception stack — so a
vision-language model can eventually drive.

```
 browser (WASD / gamepad)                    foveate (peer repo, shared bus)
        │  WebSocket                        ┌──────────────────────────────┐
        ▼                                   │  motion │ YOLO │ VLM │ fusion │
   ┌─────────┐                              └────▲──────────────────┬───────┘
   │ teleop  │ ── s1.commands ──┐        frames  │                  │ fusion
   │ :8700   │ ◄─ s1.telemetry ─┤                │                  ▼
   └─────────┘                  ▼                │           ┌─────────────┐
        ▲                ┌──────────────┐ ───────┘           │ intent loop │
        │ MJPEG          │  s1-driver   │                    │   (later)   │
        └────────────────│  (Go)        │ ◄── intentions ────└─────────────┘
                         │  safety      │
                         │  inline      │
                         └──────┬───────┘
                       UnityBridge (app mode)
                                ▼
                         ╔═════════════╗
                         ║ RoboMaster  ║  stock, unrooted, fw 00.06.0518
                         ║     S1      ║
                         ╚═════════════╝
```

Two structural ideas do most of the work here:

**`s1-driver` publishes `frames` in foveate's own schema**, so the robot's camera
enters the existing perception pipeline as just another camera. No perception
code is duplicated or forked.

**Control and video share one process** because DJI's bridge handle is
process-wide — which is also why the safety layer lives inside it, on the last
hop before the wire, where nothing can route around it.

## Status

Design phase; nothing implemented yet. M0 is done: both vehicles run firmware
00.06.0518, which closes the root-and-EP-SDK route, so we drive a **stock,
unmodified S1** by impersonating the mobile app. Next is M1 — a latency
harness, because two numbers (Wi-Fi jitter under motion, and video decode under
Rosetta) decide whether the autonomy design survives contact.

See [docs/STATUS.md](docs/STATUS.md) for where things stand and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design.

## Documentation

| Doc | What's in it |
|---|---|
| [docs/STATUS.md](docs/STATUS.md) | **Start here.** Blockers, progress, bring-up, session log |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Services, transports, command model, safety, roadmap |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Every non-obvious choice, with rationale and revisit triggers |
| [docs/HARDWARE.md](docs/HARDWARE.md) | The three transport paths, firmware triage, what to buy |
