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
        │  WebSocket                          ┌────────────────────────────┐
        ▼                                     │ capture │ motion │ YOLO    │
   ┌─────────┐   s1.commands   ┌──────────┐   │            │              │
   │ teleop  │ ──────────────► │ s1-link  │   │            ▼              │
   │ :8700   │ ◄────────────── │ (safety  │   │          VLM ──► fusion   │
   └─────────┘   s1.telemetry  │  inline) │   └────────────┬───────────────┘
        ▲                      └────┬─────┘                │ intentions
        │ MJPEG                     │ velocity cmds        │ (later)
        │                           ▼                      ▼
   ┌──────────┐  frames      ╔═════════════╗        ┌─────────────┐
   │ s1-video │ ───────────► ║ RoboMaster  ║        │ intent loop │
   │  :40921  │ ◄─── H.264 ──║     S1      ║        │  (later)    │
   └──────────┘              ╚═════════════╝        └─────────────┘
```

The key structural idea: **`s1-video` is a drop-in replacement for foveate's
capture service.** The robot's camera enters the existing pipeline as just
another camera, publishing the same `frames` schema. No perception code is
duplicated or forked.

## Status

Design phase. Nothing is implemented — the transport choice is blocked on a
hardware fact (which firmware the two vehicles are running). See
[docs/STATUS.md](docs/STATUS.md) for the blocker and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design.

## Documentation

| Doc | What's in it |
|---|---|
| [docs/STATUS.md](docs/STATUS.md) | **Start here.** Blockers, progress, bring-up, session log |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Services, transports, command model, safety, roadmap |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Every non-obvious choice, with rationale and revisit triggers |
| [docs/HARDWARE.md](docs/HARDWARE.md) | The three transport paths, firmware triage, what to buy |
