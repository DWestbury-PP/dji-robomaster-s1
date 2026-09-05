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

### `teleop` (Go, in the driver process) — the browser console (:8700)

Revised from the original Python-over-the-bus design; the rationale is measured
rather than aesthetic (DECISIONS.md #13). Serves three things:

| Route | Carries |
|---|---|
| `GET /` | the console UI, embedded in the binary |
| `GET /stream` | multipart MJPEG, encoded **once** per frame and fanned out |
| `GET /ws` | commands up, telemetry down at 10 Hz |

The browser sends *intent* — held keys, gamepad axes — never prebaked packets,
and is **never trusted to clamp**: every limit is applied server-side in the
governor. Closing the console is treated as a dead producer and handled by the
deadman, which is deliberately the same path as a browser crash or a Wi-Fi drop.

The HUD surfaces the governor's `Reason` on every tick, which turns "it won't
move" into "deadman" without a debugger.

### `s1narrate` (Go) — the observer

Its own process. Pulls a frame from `/frame.jpg` on its own cadence, asks a
local model to describe it in prose, and posts the caption to `/perception`.

It **pulls rather than being pushed to**, which is the whole reason a slow model
cannot hurt anything: however long it takes, the control loop and the video are
untouched (DECISIONS.md #14). Running on real frames with `gemma4:e4b` it
produces a caption in 1.6–3.7 s.

No schema — free prose (#16). Nothing downstream parses it and nothing acts on
it (#15), so the only measure that matters is whether the caption is true and
readable.

### intent loop — deferred

Consuming `fusion` and emitting `intentions` waits for a model fast and
spatially reliable enough to trust, which the 2026-09-05 bake-off showed does
not currently exist (DECISIONS.md #15). The transport is already in place for
when it does.

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

Measured 2026-09-04, WiFi Direct, robot stationary at close range, 60 s sample
(`runs/direct-01.json`, `runs/direct-02-rttfix.json`).

| Leg | Budget | Measured |
|---|---|---|
| Connect to ready | — | **1.8–2.2 s** |
| Time to first video frame | — | **850–880 ms** |
| Video throughput | 30 fps | **30.1 fps @ 1280×720, 0% deficit** ✅ |
| Frame interval p50 / p95 / p99 | — | **33 / 68 / 127 ms** |
| Frame interval jitter (σ) | — | **23–29 ms** ⚠️ |
| **Decode inside the emulated blob (Rosetta)** | must keep 30 fps | **0.20–0.25 cores total process CPU** ✅ |
| RGB→RGBA + JPEG encode (our cost) | < 10 ms | **23.6 ms p50** ⚠️ |
| Command issue into the bridge | — | 0.2 ms (local enqueue, not the wire) |
| **Command RTT (key to motion)** | < 150 ms | **not measurable in software** — see below |
| **Glass-to-glass video** | < 400 ms | **not measurable in software** |
| Tier-0 + fast tier (foveate, measured) | ~11 ms | ✅ |
| Slow tier / VLM (foveate, measured) | 6.5–13 s | ✅ |

**Rosetta is a non-issue.** Full 30 fps with zero deficit at a quarter of one
core. DECISIONS.md #11 stays deferred, now on evidence rather than assumption.

**Control-plane RTT cannot be measured through this bridge.** `GetKeyValueSync`
with `useCache=false` still returns in 0.2 ms p50 — DJI's library answers from
its own internal state rather than the wire, so the call never exposes link
latency. This is a property of the bridge, not a tuning problem. Real
key-to-motion needs the external method (docs/M1.md).

### Under motion — the pass that mattered

Bounded exercise running *during* sampling: forward/back, Mecanum strafes both
ways, 360° rotation, gimbal sweeps and infrared fire — 31 legs over 45 s,
**visually confirmed by the operator** (`runs/direct-05-motion-real.json`).

| Metric | Stationary | **Under full motion** |
|---|---|---|
| Throughput | 30.1 fps | **30.0 fps** |
| Frame interval p50 | 33.0 ms | **33.0 ms** |
| p95 | 63.9–68.2 ms | **65.5 ms** |
| p99 | 126.7–129.4 ms | **121.8 ms** |
| Jitter (σ) | 23.1–29.3 ms | **24.7 ms** |
| Process CPU | 0.20–0.25 cores | **0.25 cores** |

**Motion does not degrade the link.** Drive motors under load, the chassis
rotating through 360°, and the antenna continuously changing orientation moved
nothing outside the stationary range. The risk this milestone existed to find is
not there.

### Router mode — measured 2026-09-05, and it changes the picture

With the S1 joined to the house network instead of serving its own AP
(`runs/router-01.json`, 60 s, stationary):

| Metric | WiFi Direct | **Router mode** |
|---|---|---|
| Throughput | 30.1 fps | **30.1 fps** |
| Frame interval p50 | 33.0 ms | **33.3 ms** |
| p95 | 63.9–68.2 ms | **37.7 ms** |
| p99 | 121.8–129.4 ms | **42.5 ms** |
| Jitter (σ) | 21.9–29.3 ms | **3.3 ms** |
| Smallest gap | ~1 ms (bursts) | 4.3 ms |
| Time to first frame | 288–883 ms | 1420 ms |
| Process CPU | 0.24–0.25 cores | 0.25 cores |

**Jitter falls by roughly 7×, and the p99 tail by 3×.** The burstiness is
largely gone: the smallest gap moves from ~1 ms — two frames arriving
back-to-back — to a healthy 4.3 ms. Same throughput, same CPU, marginally slower
to first frame.

> **A correction.** The stationary-versus-motion comparison above concluded the
> burstiness was "not the radio: it is the decoder delivering frames in
> batches." That over-reached. Identical behaviour moving and still ruled out
> *motion* as the cause; it did not rule out the link. Changing a different
> variable — the robot's own AP for a real access point — improved jitter 7×,
> which the decoder-batching explanation cannot account for. The direct-mode
> radio was the dominant term.

**Router mode is now the default** for both binaries. It is better on every
measure that matters, removes the dual-homing arrangement entirely (one network
for Redis, Ollama and the robot), and gives whole-home range.

> **An earlier run (`runs/direct-03-motion.json`) reported the same conclusion
> and was not valid evidence for it.** The chassis never moved: commands went to
> `Chassis.SetSpeed()`, which the S1 ignores, and `DirectSendKeyValue` is
> fire-and-forget so nothing errored. Only the gimbal was moving. The conclusion
> survived re-testing, but it was not earned until the run above. See
> DECISIONS.md #12.

That also reinterprets the burstiness. It is identical moving and still, so it
is **not the radio**: it is the decoder/bridge delivering frames in batches. The
signature is a 1 ms minimum (two frames back-to-back) against a clean 33 ms
median, with p95 and p99 landing near exact multiples of the frame time.

**Consequences for the design.** The p99 gap of 127 ms sits comfortably inside
the 250 ms deadman, so this is a smoothness property, not a safety one. At
foveate's 10 fps capture rate it occasionally costs one frame. And our own
RGB→JPEG encode costs 23.6 ms against a 33 ms budget, single-threaded — fine at
10 fps, but `s1-driver` must not naively encode every frame at 30.

The reason a ~300 ms video horizon is acceptable at all: the VLM is not in the
control loop. Reflexes belong to tier 0/1 and to the safety layer; the VLM sets
intent at 0.1 Hz. A monolithic "VLM drives the wheels" design would die at these
numbers. This one does not.

## 7. Roadmap

```
[x] M0 — Firmware triage; transport selected (Path B, app-mode)
[x] M1 — Bare link + latency harness; §6 filled; Rosetta cleared on evidence
[x] M2 — Safety layer in Go with tests (deadman, clamps, arming, e-stop)
        └ hardware proof via `s1probe -safety-demo` still outstanding
[x] M3 — Browser teleop at :8700: WASD/gamepad, MJPEG view, telemetry HUD, e-stop
[x] M3.5 — Router mode adopted; jitter ~7x better than the robot's own AP
[x] M3.6 — Live speed gears; M3.7 — cockpit UI; M3.8 — movable narration
[x] M4 — Perception transport: cadence-matched frame access, dated observations
[x] M4.1 — Model bake-off on a real corpus; scene tier chosen on evidence
[ ] M4.2 — Fast tier: YOLO boxes to the console. Visualisation and logging only
[x] M4.3 — Scene tier: `s1narrate` narrating in prose on its own cadence
[x] M4.4 — The experience log: frames + narration + OPERATOR INPUT, time-aligned.
           On by default. Detections join it when M4.2 lands
[ ] M4.5 — Advisory looming highlight in the console, wired to nothing
[ ] M5 — Mobile app — decide then whether it goes through the Mac or talks to
         the S1 directly via brunoga/robomaster-mobile (gomobile)

    Deferred, not abandoned (DECISIONS.md #15):
    · reflex veto clamping the governor — needs depth or a calibrated ground plane
    · intentions schema and closed-loop autonomy — needs a model fast and
      spatially reliable enough to trust, which does not currently exist
```

M1 was a measurement milestone rather than a feature, and it earned its place:
the numbers cleared Rosetta on evidence and killed the case for the arm64 work
(#11) before three milestones were stacked on an assumption.

## 8. Known unknowns carried into M1

- **Key discovery.** The bridge API is a reverse-engineered key/value + event
  system; the library's own README says the remaining work is learning what each
  key does. M1 should produce a small map of the keys we actually need.
- **Wi-Fi mode.** Direct (S1 as AP) may force the Mac off the network that hosts
  Redis and Ollama. Router mode avoids that but adds a hop. Measure both.
- **Two vehicles, one bridge.** Whether a second S1 can be driven from the same
  host at all is unknown — the handle is process-wide, so it is at best a second
  process, at worst not supported.
