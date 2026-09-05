# Architecture

> Revised 2026-09-04 after M0. Both vehicles are on firmware 00.06.0518, which
> closes Path A (root + EP SDK). We drive a **stock, unrooted** S1 through the
> app-mode UnityBridge library — Path B. See HARDWARE.md.

## 1. Premise

The S1 is a body on a leash. Its onboard compute is not usable for our models —
every model runs on the Mac, and every command crosses Wi-Fi. The design
consequence is stated once here and assumed everywhere below:

> **We control the vehicle with rate commands under a deadman timer, never with
> open-loop position or waypoint following.** A dropped or delayed packet must
> decay to "stopped", not to "still executing the last thing you said."

The second premise, new since M0: **the DJI bridge is a single process-wide
handle.** Two processes cannot both hold it. That is not a style preference — it
collapses what were two services into one, and it decides where safety lives.

## 2. Relationship to foveate

`foveate` is a peer repo, not a dependency in the vendoring sense. It owns
perception; this repo owns the vehicle. They meet on the Redis Streams bus.

| Stream | Producer | Consumer | Schema owner |
|---|---|---|---|
| `frames` | **`s1-driver`** (this repo) | foveate tiers | foveate |
| `detections`, `observations`, `fusion` | foveate | this repo's intent loop (later) | foveate |
| `intentions` | intent loop (later) | `s1-driver` | **joint** — foveate M10 |
| `s1.commands` | `teleop`, intent loop | `s1-driver` | this repo |
| `s1.telemetry` | `s1-driver` | `teleop`, intent loop | this repo |

`s1-driver` publishes foveate's `FrameMessage` verbatim with
`camera_id="s1-fpv"`, writing JPEGs into foveate's frame store. From foveate's
side the robot is indistinguishable from a webcam — which is the point, and
which is why foveate's M8 (multi-camera) is the prerequisite worth landing.

The `frames` schema is foveate's to change. This repo pins a copy and carries a
contract test that fails loudly on drift (DECISIONS.md #4).

## 3. Services

Three processes: one Go, two Python.

### `s1-driver` (Go) — the only thing that touches the robot

Holds the single UnityBridge handle, and therefore owns **both** control and
video. Consumes `s1.commands`, publishes `s1.telemetry` and `frames`.

```
 s1.commands ──►┌──────────────────────────────────┐
                │  safety (deadman, clamps, arming)│  ◄── unbypassable: no
                ├──────────────────────────────────┤      other path exists
                │  brunoga/robomaster              │
                │  module/{chassis,gimbal,gun,…}   │──► robot
                │  module/camera → RGB callback    │◄── robot
                ├──────────────────────────────────┤
                │  JPEG encode → foveate frame store│
                └──┬────────────────┬──────────────┘
       s1.telemetry ▼              ▼ frames
```

Runs as `GOARCH=amd64` under Rosetta 2 — there is no arm64 build of DJI's
library (HARDWARE.md, Path B). It is I/O-bound, so emulation is cheap
everywhere except video decode, which M1 measures.

Because the camera hands us **decoded RGB via callback**, there is no H.264
parsing on our side: the frame path is callback → JPEG → frame store → `frames`.

Talking Redis from Go rather than shimming through a Python parent is
deliberate (DECISIONS.md #9): it removes a hop, and a hop in front of the
safety layer is a hop that can be routed around.

### `teleop` (Python) — the browser console (:8700)

FastAPI. WebSocket carries commands up and telemetry down at ~20 Hz; video
returns as MJPEG in v1 (WebRTC is a revisit trigger, not a v1 requirement —
DECISIONS.md #5). The browser sends *intent* (held keys, gamepad axes), not
prebaked packets; the server converts to rate commands. That keeps the wire
format free to change and makes the future mobile app the same WebSocket.

### intent loop (Python) — later (M5+)

Consumes `fusion`, emits `intentions`, which `s1-driver` executes under the same
safety layer as a human. It gets no privileged path.

## 4. Command model

One message type, rate-based, with an explicit expiry:

```
CommandMessage:
  ts_ms:     int
  source:    "human" | "intent"        # authority, not decoration
  ttl_ms:    int                        # default 250
  vx, vy:    float                      # chassis translation, m/s
  omega:     float                      # chassis rotation, deg/s
  gimbal_pitch_rate, gimbal_yaw_rate: float
  fire:      int = 0                    # blaster rounds; gated, see §5
```

`s1-driver` applies the newest non-expired command each tick. An expired command
is not "the last known good value" — it is zero.

## 5. Safety layer

Implemented **in Go, inside `s1-driver`**, before the first drive:

1. **Deadman.** No fresh command within `DEADMAN_MS` (default 250) → chassis
   and gimbal rates go to zero. Independent of, and additional to, per-command
   TTL: TTL covers a stale command, the deadman covers a dead producer.
2. **Speed clamp.** `MAX_VX/VY/OMEGA` from config, applied last thing before the
   bridge call. The browser is never trusted to clamp.
3. **Blaster interlock.** `fire > 0` is dropped unless the driver is in `armed`
   state. Arming requires an explicit, separate operator action, expires after
   `ARM_TTL_S` (default 30), and **is refused for `source="intent"` in v1** —
   no model actuates the blaster. Revisiting that is a deliberate decision with
   its own sign-off, not a config change.
4. **E-stop.** A dedicated path that zeroes rates, disarms, and holds until
   explicitly cleared. Reachable from the browser as a key and a button.
5. **Link loss.** Bridge error or telemetry silence → same as deadman, plus a
   reconnect with backoff. Reconnect never restores `armed`.

Putting this in Go rather than in a Python supervisor is the whole reason
`s1-driver` speaks Redis directly. Safety on any hop but the last is advisory.

## 6. Latency budget

All figures **unmeasured**. Filling this table is milestone M1, and the numbers
decide whether the autonomy design survives contact.

| Leg | Budget | Measured |
|---|---|---|
| Browser → teleop (LAN WebSocket) | < 10 ms | — |
| teleop → s1-driver (bus) | < 5 ms | — |
| s1-driver → robot actuation (Wi-Fi + bridge) | ? | — |
| **Video decode inside the emulated blob (Rosetta)** | **?** | — |
| RGB → JPEG → frame store | < 10 ms | — |
| **Command RTT (key to motion)** | **< 150 ms** | — |
| **Glass-to-glass video** | **< 400 ms** | — |
| Tier-0 + fast tier (foveate, measured) | ~11 ms | ✅ |
| Slow tier / VLM (foveate, measured) | 6.5–13 s | ✅ |

Two rows are the ones that can kill the design: **Wi-Fi jitter under motion**,
and **decode cost under Rosetta**. Everything else is LAN-local and cheap.

The reason a ~300 ms video horizon is acceptable at all: the VLM is not in the
control loop. Reflexes belong to tier 0/1 and to the safety layer; the VLM sets
intent at 0.1 Hz. A monolithic "VLM drives the wheels" design would die at these
numbers. This one does not.

## 7. Roadmap

```
[x] M0 — Firmware triage; transport selected (Path B, app-mode)
[ ] M1 — Bare link + latency harness; fill §6; go/no-go on Rosetta decode
[ ] M2 — Safety layer in Go with tests (deadman, clamps, arming, e-stop)
[ ] M3 — Browser teleop at :8700: WASD/gamepad, MJPEG view, telemetry HUD, e-stop
[ ] M4 — s1-driver publishing `frames`; foveate narrates the robot's view
[ ] M5 — Intentions schema, designed jointly with foveate M10
[ ] M6 — Closed-loop autonomy v1: navigate by obstacle class from `fusion`
[ ] M7 — Mobile app — decide then whether it goes through the Mac or talks to
         the S1 directly via brunoga/robomaster-mobile (gomobile)
```

M1 is a measurement milestone, not a feature. If the numbers come back bad, we
learn it before building three milestones on top of them.

## 8. Known unknowns carried into M1

- **Key discovery.** The bridge API is a reverse-engineered key/value + event
  system; the library's own README says the remaining work is learning what each
  key does. M1 should produce a small map of the keys we actually need.
- **Wi-Fi mode.** Direct (S1 as AP) may force the Mac off the network that hosts
  Redis and Ollama. Router mode avoids that but adds a hop. Measure both.
- **Two vehicles, one bridge.** Whether a second S1 can be driven from the same
  host at all is unknown — the handle is process-wide, so it is at best a second
  process, at worst not supported.
