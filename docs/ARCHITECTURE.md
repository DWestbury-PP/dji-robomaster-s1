# Architecture

## 1. Premise

The S1 is a body on a leash. Its onboard compute is not usable for our models —
every model runs on the Mac, and every command crosses Wi-Fi. The design
consequence is stated once here and assumed everywhere below:

> **We control the vehicle with rate commands under a deadman timer, never with
> open-loop position or waypoint following.** A dropped or delayed packet must
> decay to "stopped", not to "still executing the last thing you said."

## 2. Relationship to foveate

`foveate` is a peer repo, not a dependency in the vendoring sense. It owns
perception; this repo owns the vehicle. They meet on the Redis Streams bus.

| Stream | Producer | Consumer | Schema owner |
|---|---|---|---|
| `frames` | **`s1-video`** (this repo) | foveate tiers | foveate |
| `detections`, `observations`, `fusion` | foveate | this repo's intent loop (later) | foveate |
| `intentions` | intent loop (later) | `s1-link` | **joint** — foveate M10 |
| `s1.commands` | `teleop`, intent loop | `s1-link` | this repo |
| `s1.telemetry` | `s1-link` | `teleop`, intent loop | this repo |

`s1-video` publishes foveate's `FrameMessage` verbatim with
`camera_id="s1-fpv"`, writing JPEGs into foveate's frame store. From foveate's
side the robot is indistinguishable from a webcam — which is the point, and
which is why foveate's M8 (multi-camera) is the prerequisite worth landing.

The `frames` schema is foveate's to change. This repo pins a copy and carries a
contract test that fails loudly on drift (DECISIONS.md #4).

## 3. Services

Four processes. Each is a peer of foveate's services and follows the same
conventions: env-var config, a line in a `services.manifest`, one log file.

### `s1-link` — the only thing that touches the robot

Owns the robot session, exclusively. Consumes `s1.commands`, publishes
`s1.telemetry`, and holds the driver behind an interface:

```
class S1Driver(Protocol):
    def connect(self) -> None
    def chassis_velocity(self, vx, vy, omega) -> None   # m/s, m/s, deg/s
    def gimbal_rate(self, pitch, yaw) -> None           # deg/s
    def blaster_fire(self, count) -> None
    def telemetry(self) -> Iterator[TelemetryMessage]
    def stop(self) -> None
```

`EPSdkDriver` (Path A, TCP 40923/40925) and `AppModeDriver` (Path B, Go
sidecar) both satisfy it. **The firmware question therefore blocks one file,
not the design.**

**Safety lives inside this process, as a library on the last hop before the
wire — not as a separate service.** A safety service would be a hop that a
buggy or clever consumer could route around. Here it cannot be bypassed,
because there is no other path to the robot.

### `s1-video` — H.264 → foveate's frame store

Pulls the video socket, decodes, writes JPEG to the frame store, computes the
tier-0 motion score, publishes `frames`. Deliberately a near-copy of foveate's
capture service with the AVFoundation source swapped for a socket — same
output contract, so foveate needs no changes to consume the robot's eye.

### `teleop` — the browser console (:8700)

FastAPI. WebSocket carries commands up and telemetry down at ~20 Hz; video
returns as MJPEG in v1 (WebRTC is a revisit trigger, not a v1 requirement —
DECISIONS.md #5). The browser sends *intent* (held keys, gamepad axes), not
prebaked packets; the server converts to rate commands. That keeps the wire
format free to change and makes the future mobile app the same WebSocket.

### intent loop — later (M5+)

Consumes `fusion`, emits `intentions`, which `s1-link` executes under the same
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

`s1-link` applies the newest non-expired command each tick. An expired command
is not "the last known good value" — it is zero.

## 5. Safety layer

Non-negotiable, and implemented before the first drive:

1. **Deadman.** No fresh command within `DEADMAN_MS` (default 250) → chassis
   and gimbal rates go to zero. Independent of, and additional to, per-command
   TTL: TTL covers a stale command, the deadman covers a dead producer.
2. **Speed clamp.** `MAX_VX/VY/OMEGA` from config, applied after the driver
   boundary check and before the wire. The browser is never trusted to clamp.
3. **Blaster interlock.** `fire > 0` is dropped unless the link is in `armed`
   state. Arming requires an explicit, separate operator action, expires after
   `ARM_TTL_S` (default 30), and **is refused for `source="intent"` in v1** —
   no model actuates the blaster. Revisiting that is a deliberate decision with
   its own sign-off, not a config change.
4. **E-stop.** A dedicated path that zeroes rates, disarms, and holds until
   explicitly cleared. Reachable from the browser as a key and a button.
5. **Link loss.** Socket error or telemetry silence → same as deadman, plus a
   reconnect with backoff. Reconnect never restores `armed`.

## 6. Latency budget

All figures **unmeasured**. Filling this table is milestone M1, and the numbers
decide whether the autonomy design survives contact.

| Leg | Budget | Measured |
|---|---|---|
| Browser → teleop (LAN WebSocket) | < 10 ms | — |
| teleop → s1-link (bus) | < 5 ms | — |
| s1-link → robot actuation (Wi-Fi) | ? | — |
| Robot camera → s1-video decoded frame | ? | — |
| **Command RTT (glass to motion)** | **< 150 ms** | — |
| **Glass-to-glass video** | **< 400 ms** | — |
| Tier-0 + fast tier (foveate, measured) | ~11 ms | ✅ |
| Slow tier / VLM (foveate, measured) | 6.5–13 s | ✅ |

The reason a ~300 ms video horizon is acceptable: the VLM is not in the control
loop. Reflexes belong to tier 0/1 and to the safety layer; the VLM sets intent
at 0.1 Hz. A monolithic "VLM drives the wheels" design would die at these
numbers. This one does not — provided **Wi-Fi jitter under motion** stays
bounded, which is the actual risk and the actual thing M1 measures.

## 7. Roadmap

```
[ ] M0 — Firmware triage on both vehicles; pick the transport   ← BLOCKED, hardware
[ ] M1 — Bare link + latency harness; fill §6; transport go/no-go
[ ] M2 — Safety layer with tests (deadman, clamps, arming, e-stop)
[ ] M3 — Browser teleop at :8700: WASD/gamepad, MJPEG view, telemetry HUD, e-stop
[ ] M4 — s1-video publishing `frames`; foveate narrates the robot's view
[ ] M5 — Intentions schema, designed jointly with foveate M10
[ ] M6 — Closed-loop autonomy v1: navigate by obstacle class from `fusion`
[ ] M7 — Mobile app on the same WebSocket
```

M1 is a measurement milestone, not a feature. If the numbers come back bad, we
learn it before building three milestones on top of them.
