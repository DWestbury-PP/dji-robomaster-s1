# Decisions

Every non-obvious choice, with the reasoning and the condition that would make
us revisit it. Same format as foveate's DECISIONS.md.

---

### 1. Peer repo on a shared bus, not a fork of or a module inside foveate

**Decision.** `dji-robomaster-s1` is its own repo. It meets foveate on the Redis
Streams bus: it publishes `frames`, it consumes `fusion`.

**Why.** foveate is a general perception stack whose value is being
vehicle-agnostic; folding S1 services into it would couple it to one specific
robot and drag DJI hack tooling into a repo that may want to stay publishable.
Separation also means a second vehicle later is a second repo, not a second
branch of foveate.

**Cost, stated honestly.** One cross-repo contract (the `frames` schema) and two
stacks to bring up. Mitigated by #4.

**Revisit if.** The contract churns often enough that the two repos are being
edited in lockstep anyway — at that point the separation is fiction.

---

### 2. Teleop before autonomy

**Decision.** v1 is a human driving the robot from a browser with a real safety
layer. No model in the loop.

**Why.** Teleop is what actually measures the link, proves the latency budget,
and forces the safety layer into existence. Every autonomy milestone sits on
those three things. It is also the milestone that is immediately fun, which is
not a trivial consideration for a project whose failure mode is going back on
the shelf.

**Revisit if.** M1 shows the command RTT is bad enough that human teleop is
unpleasant — in which case the answer is probably Path C, not "skip to autonomy."

---

### 3. Rate commands under a deadman, never position or waypoints

**Decision.** The only chassis/gimbal primitive is a velocity with a TTL.

**Why.** Every command crosses Wi-Fi to a vehicle we cannot run code on. Under
packet loss, a rate command decays safely to zero; a position command keeps
executing into whatever it was about to hit. This is also why the S1's own
`chassis move` (distance-based) API is deliberately unused.

**Revisit if.** We move to Path C and gain a real-time link with local control,
where closed-loop position on the vehicle becomes safe.

---

### 4. Vendor foveate's `frames` schema, with a contract test

**Decision.** Copy foveate's `FrameMessage` into this repo rather than importing
foveate as a path dependency. Add a test that reads foveate's
`services/shared/schemas.py` when present at `FOVEATE_PATH` and fails on drift.

**Why.** A path dependency across two independently-versioned personal repos
makes each one un-buildable without the other, for one small model. A vendored
copy plus a drift test gets independence and catches the failure the dependency
was protecting against — loudly, in CI, instead of at runtime with a malformed
frame.

**Revisit if.** More than two or three schemas end up shared; at that point it
wants a real published contract package.

---

### 5. MJPEG for the browser video in v1; WebRTC is a revisit trigger

**Decision.** The teleop console gets its video as MJPEG over HTTP.

**Why.** It is a few lines, has no signalling, no STUN/TURN, no codec
negotiation, and works in every browser. On a LAN, its latency is dominated by
the S1's own link, which we do not control. Adding WebRTC in v1 spends the
project's early complexity budget on the leg that is not the bottleneck.

**Revisit if.** M1 measurements show the browser leg is a material share of
glass-to-glass, or the mobile app needs to work off-LAN.

---

### 6. Safety is a library inside `s1-driver`, not a service

**Decision.** Deadman, clamps, arming and e-stop are enforced in-process on the
last hop before the wire. Post-M0 that means **written in Go**, inside the
process that holds the bridge handle.

**Why.** A safety *service* is a hop, and a hop is something a future consumer
can be written around — including by us, at 1 a.m., "just to test something."
Inside `s1-driver` there is no other path to the robot, so the guarantee holds by
construction rather than by discipline. The tempting alternative — a Python
supervisor in front of a dumb Go transport — puts safety on the second-to-last
hop, which is advisory. Rejected for that reason.

**Revisit if.** Never, ideally. If a second process ever needs to talk to the
robot, it goes through `s1-driver`.

---

### 7. No model actuates the blaster in v1

**Decision.** `fire > 0` is refused when `source="intent"`. Arming is a
human-only, explicitly-expiring action.

**Why.** This is a projectile weapon operating in a house, aimed by a system
whose perception layer we are still calibrating and whose stated purpose is
recognizing *obstacle classes* — a category that includes pets and people. The
interlock costs nothing now and is much harder to add after the autonomy code
assumes it can fire.

**Revisit if.** Deliberately, with its own sign-off, after M6 — and probably
still not in a room with living things in it.

---

### 8. ~~Root one vehicle, keep one stock~~ — SUPERSEDED by M0

**Superseded 2026-09-04.** Both vehicles came back on firmware 00.06.0518, the
version that closes the root hack, with 00.06.0521 staged. There is nothing to
root and no version split to exploit. Path A is off the table
(HARDWARE.md §RESULT).

**What replaces it.** Path B (app-mode via UnityBridge) on vehicle 1, which
needs no modification at all. Vehicle 2 stays pristine as the control, and
becomes the Path C (CAN bus) testbed if and when we want unrestricted authority.

**Standing rule that survives.** Do not let 00.06.0521 install on either unit.

---

### 9. `s1-driver` is one Go process that speaks Redis directly

**Decision.** Control and video collapse into a single Go binary that consumes
`s1.commands` and publishes `s1.telemetry` and `frames` itself, rather than a Go
transport sidecar behind a Python `s1-link`.

**Why.** Two reasons, one hard and one soft. The hard one: **the UnityBridge
handle is process-wide**, so a separate video service physically cannot hold its
own connection — link and camera must share a process. The soft one: any Python
wrapper in front of it would sit *upstream* of the safety layer, which is
exactly the arrangement #6 rejects.

**Cost, stated honestly.** Go enters the stack, and the safety layer gets
written in a second language rather than reusing Python. Accepted: it is ~150
lines, and it is the one place where being on the last hop matters more than
language uniformity.

**Revisit if.** We move to Path C, where we own the transport end to end and the
process-wide-handle constraint disappears.

---

### 10. Accept the Rosetta 2 dependency, with an exit

**Decision.** Build `s1-driver` as `GOARCH=amd64` and run it under Rosetta 2 on
the M-series Mac.

**Why.** DJI never shipped an arm64 macOS build of the UnityBridge library and
never will; the upstream repo handles Apple Silicon exactly this way. The
process is I/O-bound, so emulation is cheap — with one exception worth
measuring, video decode, which happens inside the emulated blob instead of on
VideoToolbox (ARCHITECTURE.md §6).

**The risk.** Apple has signalled Rosetta 2 will be pared back after macOS 27.
This Mac is on macOS 26.

**The exit, and why it is cheap.** `s1-driver` is a small, self-contained
process with a bus interface and no GPU work. If Rosetta goes away it relocates
to a Linux amd64 host (upstream runs the Windows DLL under Wine there) or to an
Intel box on the LAN, and *nothing else in either repo changes* — foveate keeps
running natively on the Mac. The bus is what makes this a packaging problem
instead of a rewrite.

**Revisit if.** M1 shows decode under Rosetta is a material share of the video
budget, or macOS 27 lands on this machine.

---

### 11. Stay on Rosetta for now; the native-arm64 route is understood but deferred

**Decision.** Ship `s1-driver` as amd64 under Rosetta 2 (#10). Do not attempt a
native arm64 bridge before M1.

**Why.** A 20-minute spike (SPIKE-arm64-bridge.md) established that DJI's iOS
build *is* arm64, is *not* DRM-encrypted, exports the same 11-function API, and
re-platforms to Mac Catalyst in a single `vtool` command — but dyld requires the
**loading process** to be Catalyst too, and neither Go nor Command Line Tools
clang can produce one. The only shape that works is a Catalyst host process
talking to Go over IPC: 3–4 days best case, 1–2 weeks realistic, with a real
chance the Unity plugin will not run headless at all.

Against that: we have **not measured what Rosetta costs us** — M1 does. And the
fix would *add* a process and an IPC hop, so the net win is not certainly
positive. Optimizing an unquantified cost, at that price, is the wrong order.

**Not a supporting argument.** The Jetson/M9 case does not transfer — arm64
Linux needs the *Android* ELF `.so`, a different artifact with different
blockers (SPIKE-arm64-bridge.md §6).

**Revisit if.** M1 shows decode under Rosetta is a material share of the
glass-to-glass budget, or macOS 27 lands on this machine (#10). The spike doc is
the head start.

---

### 12. Movement goes through `Controller.Move`, not `Chassis.SetSpeed`

**Decision.** All chassis motion is issued as virtual-stick input via
`Controller.Move(chassisStick, gimbalStick, ModeFPV)` — the path the DJI mobile
app itself uses (`KeyMainControllerVirtualStick`) — refreshed continuously at
~20 Hz. `Chassis.SetSpeed()` is not used.

**Why.** The S1 silently ignores `Chassis.SetSpeed()`. It writes a different key,
and because the underlying `DirectSendKeyValue` is **fire-and-forget with no
acknowledgement**, the call returns `nil` whether or not the robot does anything.
A full motion run reported 39 successful legs while the vehicle sat still; only
the operator watching it caught the discrepancy.

**Two things this costs us, permanently:**

1. **Stick values are fractions of full deflection in [-1, 1], not m/s.** Speed
   limiting is a deflection clamp plus the chassis speed level, not a velocity
   in physical units. The safety layer's "speed clamp" (§5) must be written in
   those terms.
2. **No command is ever acknowledged.** We cannot confirm from software that the
   robot acted. Any future claim that a command took effect must come from
   telemetry or from a human, never from a nil error.

**The compensation.** The vehicle *watchdogs* the virtual stick: a single send
does nothing, and motion stops when the stream lapses. That is the hardware
independently enforcing #3 — rate commands under a deadman is not merely our
safety preference, it is the only thing the robot responds to.

**Revisit if.** We move to Path C, where we own the wire and can have real
acknowledgements.

---

### 13. The teleop console is served from the Go driver process

**Decision.** `s1teleop` serves MJPEG, the command/telemetry WebSocket and the
static UI from the same process that holds the DJI bridge. It does not go
through Redis, and it is not the Python FastAPI service #1 originally implied.

**Why.** Three measured facts, none of which were known when the design was
written:

1. **The bridge handle is process-wide** (#9), so decoded frames are already in
   the Go process. Any bus-mediated route means getting them *out* again.
2. **JPEG encode costs 23.6 ms per frame** (ARCHITECTURE.md §6). Routing video
   Go → frame store → Python → browser pays that twice, plus a disk round trip,
   on the most latency-sensitive path in the system.
3. The safety governor is already in Go and must stay on the last hop (#6). A
   Python console would sit upstream of it regardless, so moving the console
   does not buy any safety property.

Adding Redis and Docker as prerequisites for *manual control* is also the wrong
dependency shape: the console should work when the perception stack is down.

**What this does not change.** The bus still earns its place for the two things
it was chosen for — publishing `frames` to foveate, and carrying `intentions`
from the autonomy loop (M5+). Those are throughput-tolerant and genuinely
cross-process. The human control path simply should not pay for them.

**Consequence worth naming.** The console encodes **once per published frame**
regardless of how many browsers are watching, and drops frames between encodes
rather than queueing — a teleop view wants the newest frame, never a backlog.
Browser stream rate defaults to 15 fps because 30 would spend most of a core on
encoding alone.

**Revisit if.** A second consumer needs the same video, or the console has to be
reachable from somewhere the driver process is not.

---

### 14. Perception talks HTTP to the driver, not Redis

**Decision.** Tiers pull frames from `s1teleop` over HTTP — `/stream` for
consumers that keep up, `/frame.jpg` for those that do not — and post results
back to `/perception`. No broker, no disk frame store.

**Why.** The bus exists to stop a slow consumer gating a fast one and to share
pixels between processes. We get the first from `FrameHub`, which already lets
each consumer take the newest frame independently while the encoder waits on
nobody. We do not need the second: the frames are already encoded once for the
browser, so a bus route would pay the 23.6 ms encode twice and add a disk round
trip, on a robot that otherwise needs neither Docker nor Redis.

**The part that is not obvious.** Push and pull are not interchangeable here. A
pushed stream buffers frames in the socket while a slow tier thinks, so an 8.5 s
model would reason about a scene from 8 seconds ago — the exact coupling the bus
was meant to remove, smuggled back in by the transport. Slow tiers must pull.

**What this costs.** Replay, durability and consumer groups. Fine today; if we
want to re-run a model over a recorded drive, a broker earns its place, and the
observation schema is what we would replay into it.

**Not adopted from foveate, deliberately.** Its capture service (we have the
camera), its frame store (we have the frames), its monitor (we have a better
console), and its process-per-tier stack. **Adopted:** the tiering idea, the
fusion principle, the schema vocabulary, and above all its **benchmarks** —
which are why we start from yolo11s on ONNX/CoreML and qwen2.5vl:7b rather than
re-deriving both.

**Revisit if.** Perception moves to another host and needs reconnect semantics,
or we want recorded drives replayed through new models.
