# Explorations

**Ideas, not commitments.** Nothing here is scheduled, promised, or being
worked on unless [STATUS.md](STATUS.md) says so. This file exists because the
interesting part of a hobby platform is usually the set of things it *could*
do, and because most of the cost of trying one is finding out what is already
known. That part is written down here so the next person — us later, or you —
starts from the evidence instead of from scratch.

Each entry says what it would unlock, what is actually verified, what is only
believed, and what it would cost. Where something was measured, the number is
here. Where it was not, it says so.

Decisions that have been *made* live in [DECISIONS.md](DECISIONS.md), and that
file is deliberately not speculative. This one is the opposite: none of it is
decided.

**If you want to try one of these, please do.** Fork it, open an issue, or send
a PR. The two things worth preserving are in DECISIONS.md #6 and #15 — keep the
governor on the last hop before the wire, and do not let a model's output drive
the wheels without being able to say why it earned that.

---

## What the robot will actually tell you

Most ideas below live or die on this table, so it comes first. From the
`brunoga/robomaster` bridge on stock firmware:

| Signal | State |
|---|---|
| Camera frames (decoded RGB via callback) | available, used |
| Battery, connection, device list | available, used |
| `KeyGimbalAttitude` — where the turret points | **available and decoded** |
| `KeyRobomasterMainControllerRelativePosition` | readable, **value type undecoded** — raw bytes |
| `KeyVision*` — the S1's native marker, tracking and detection results | present, **all undecoded** |
| `KeyRobomasterTOF*` — the distance-sensor subsystem | present in firmware, **answers `-1` unsupported** |

A note on reading that last row, because it is the difference between "try
harder" and "stop": an *unsupported* key and an *empty* one look nothing alike.
Unsupported answers `Error: 0xFFFFFFFF` with no value; a supported key answers
`Error: 0` even when it has nothing to report. `cmd/s1tof` asks, safely.

Two consequences worth internalising before planning anything:

**There is no usable chassis odometry.** The position key exists but the
library does not decode it, and the S1 rides on Mecanum wheels, which slip by
design. Even fully decoded, dead reckoning on carpet would drift badly and on
grass would be hopeless.

**The S1's own vision system is right there and unreachable.** The firmware
detects markers, tracks targets and finds other robots. Every one of those keys
comes back as bytes nobody has mapped. Decoding them is a real, self-contained
reverse-engineering project, and it would be a genuine contribution to anyone
building on this library.

---

## Knowing where the other robot is

**What it would unlock.** A beacon on the video showing where another vehicle
is — the thing that turns two robots on one network into something worth
playing with.

This is three different problems wearing one name, and only the last needs a map.

### Tier 1 — "I can see you". No map, no odometry.

Put a printed ArUco tag on each robot. `cv2.aruco` is already in the detector's
dependency tree via Ultralytics, and with a known tag size it returns bearing
and range **in camera frame** — drift-free, exact, and needing nothing else.

*Cost:* a printed tag, one camera-calibration session, ~100 lines in a tier
that already exists. *Limitation:* only while the other robot is in frame.
*Status:* nothing built. This is the obvious first step and it de-risks Tier 2,
which needs the same calibration and the same pipeline.

### Tier 2 — "somewhere over there". A shared frame, and where a map may appear.

An off-screen direction arrow needs both robots in one coordinate system. This
entry originally said that meant **tags at measured fixed positions**, because
Mecanum wheels slip by design and wheel odometry drifts too fast to dead-reckon
between them. That is still true of *wheel* odometry. It is not the only kind.

**Optical flow measures ground velocity optically, and does not care what the
wheels are doing.** A PMW3901-class sensor looking at the floor reports how fast
the floor is moving underneath it. Slip is invisible to it, because it never
consults a wheel. That removes the specific reason anchors looked mandatory.

Flow has two classic weaknesses — unknown scale, and varying height above the
surface — and **both largely vanish on a ground robot**, which sits at a fixed,
known height on a textured floor. These are better conditions than the sensor
was designed for. Pair it with a downward rangefinder and the scale is pinned
too.

*What is still unproven:* how far the estimate drifts over minutes of real
driving, which is the number this would have to produce before any beacon is
placed from it. Flow integrates velocity, so it accumulates error like any dead
reckoning — more slowly than wheels, not never. Tags may still earn their place
as an occasional re-anchor rather than as the primary mechanism.

*Where flow gives nothing:* a featureless floor, and darkness.

### Tier 3 — monocular SLAM.

On a 120° fisheye at 15 fps over Wi-Fi, decoded under Rosetta, with no depth
sensor. Recorded for completeness. We do not think this is the right trade.

### A principle either tier has to carry

**A beacon must show its uncertainty.** This platform already dates every
observation (#18) and refuses to let models actuate (#15) for the same reason:
a confidently wrong answer is worse than a visibly uncertain one. A beacon
placed from a 30-second-old fix should fade, grow, or go dashed. One that says
"teammate 2 m ahead" when they are behind a wall has done real harm.

---

## Bolt-on sensors

**What it would unlock.** The S1 exposes no depth, and DECISIONS.md #15 names
that as the specific thing blocking automated movement triggers — *"a better
detector does not unlock triggers; a depth sensor or a calibrated ground plane
would"* — with **"a depth sensor is added"** written down as a revisit
condition. So this is not a garnish; it is the named trigger for revisiting the
most consequential decision in the repo.

### Answered first: the S1 has no distance sensor, and cannot take DJI's

The firmware carries a complete time-of-flight subsystem — `TOFConnection`,
`TOFOnlineModules`, `TOFInfoSubscribe`, `EnableTOFInfoSubscribe`, and **four**
`TOFFirmwareVersion` keys, behind `RMTOFParamInfoSubscribeMsg`. That is DJI's
distance-sensor accessory from the RoboMaster EP line, and it looked like a far
better answer than a bolt-on pod: readings over the bridge we already speak, on
the robot's own power, with no extra Wi-Fi client competing with the video.

**The robot says no.** Asked directly, every TOF key answers
`Error: 0xFFFFFFFF` with an empty value:

| key | result |
|---|---|
| `KeyRobomasterTOFConnection` | `-1`, empty |
| `KeyRobomasterTOFOnlineModules` | `-1`, empty |
| `KeyRobomasterTOFFirmwareVersion1` | `-1`, empty |
| `KeyRobomasterTOFFirmwareVersion2` | `-1`, empty |

That is *unsupported*, not *nothing attached*. Other keys in the same session
answered `Error: 0` with real values — gimbal attitude, battery percentage — so
the robot distinguishes the two clearly. The S1 does not implement the
subsystem, and no accessory will change that.

So the pod described above stands as the way to get depth. Nothing was spent
finding this out.

**`cmd/s1tof` is the probe**, and the technique generalises to any undecoded
key. Reading one is not free: the reply is dispatched through
`result.NewFromJSON`, which calls `key.ResultValue()` — and that panics for a
key with no decoded type. Two things make it safe:

  - **Pass a nil callback.** In `notifyCallbacks` the decode sits inside
    `if c != nil`, so a nil callback means the reply is never decoded.
  - **Read the answer from the trace log.** `eventCallback` traces the raw
    bytes before dispatching, at `LevelTrace` — which is *below* Debug, so
    `-v` is not low enough.

`AddEventTypeListener` looks like the obvious raw path and is not: `eventCallback`
returns early for `TypeGetValue`, with a `TODO` upstream acknowledging it.

### The architecture that seems right

An **independent pod** — microcontroller, sensors, its own power, its own
Wi-Fi — that posts to `/perception` like any other producer. It touches no DJI
firmware, which matters on a robot carrying a staged update nobody wants
installed, and it can be built and tested with no robot present.

**Power is the one tap worth considering.** The photographed expansion bay
exposes a `POWER` pin alongside `M BUS`, `CAN BUS`, `UART`, `PWM OUTPUT` and
`S-BUS`. Taking supply from the S1's own 3S pack would remove the ongoing cost
named below — a second battery to charge and track — without any of the protocol
questions, because power is just power. Tapping a *data* bus buys nothing by
comparison: a sensor wired to that UART talks to the robot's controller, which
has no route to forward it to us (see the Sensor Adapter question above).

This is the existing seam (#14, #20): another process with its own runtime and
cadence, posting dated observations. Two constraints carry over — readings need
**dating** like everything else (a microcontroller has no reliable clock), and
the payload must stay **tiny**. Router mode measured **3.3 ms σ** jitter; that
is a baseline to protect, and adding chatty 2.4 GHz clients alongside the video
is exactly the sort of thing this project measures rather than assumes.

### On the sensors themselves

**Time-of-flight beats ultrasonic** for the proximity job. An HC-SR04 is a ~15°
cone at ~60 ms per ping, units cross-talk, and soft angled surfaces — grass —
absorb the pulse. A VL53L1X is ~4 m at 50 Hz with none of that. Ultrasonic's
real edge is glass and mirrors, where ToF struggles.

**GPS is the wrong instrument here, and the numbers say so plainly.** Consumer
GPS is 2–5 m CEP. An S1 is 320 mm long. The error would be ten times the
vehicle, and indoors there is no signal at all. RTK reaches 1–2 cm but wants a
base station, a correction link and clear sky.

**UWB is what GPS is being asked to be.** DW1000/DW3000-class modules give
~10 cm, indoors and out, through obstacles and in the dark. Four anchors at
measured positions put every robot in a shared frame continuously — the Tier 2
map, without needing line of sight to a tag.

*Honest cost:* roughly $60–70 a vehicle plus anchors, but the money is not the
real price. **You take on a second embedded platform** — firmware, mounts, and
another battery to charge. *Status:* nothing built, nothing ordered.

### Borrowing from the drone ecosystem

Hobby aircraft solved "small, light, integrated sensor package" years ago, and
most of that work transfers. But the obvious thing to reach for is the wrong
one.

**A flight controller is the wrong shape.** Most of its value is motor control
and a barometer, and we need neither — the S1 drives itself, and baro altitude
is meaningless on a floor. More decisively, **it does not solve the problem that
actually matters**: an FC has UARTs, I2C and SPI but no network. Data still has
to reach the Mac, and the robot will not carry it for us, so an ESP32 ends up
bolted on anyway — two boards where one would do.

**The sensor modules are the jump-start.** The value is not the board, it is
inheriting working, calibrated drivers instead of writing them.

| what we want | what the ecosystem sells |
|---|---|
| Distance ahead | ToF rangefinder — TF-Luna class (~8 m, UART) or VL53L1X (~4 m, I2C) |
| Ground velocity without wheel slip | Optical flow — PMW3901 class |
| **Both, one board, one UART** | **Matek 3901-L0X class** — flow + rangefinder, ~3 g, ~$25 |
| Absolute heading | GPS+compass combo module — wanted for the magnetometer, not the GNSS |
| Where the *other* robot is | **nothing** — drones do not need robot-to-robot ranging. Still UWB. |

A flow-plus-rangefinder board is the strongest single candidate here, because it
answers two open questions with one part: depth ahead for DECISIONS.md #15, and
slip-free velocity for Tier 2 above.

**The shape that follows:**

```
 flow + rangefinder board ──UART──► ESP32 ──Wi-Fi──► POST /perception
```

The ESP32 does almost nothing — read a serial protocol, post JSON — which is a
far smaller firmware job than driving sensors directly.

**One wrinkle before ordering:** these boards speak **MSP**, Betaflight and
iNav's protocol, not plain serial numbers on a wire. MSP is documented and
straightforward, but it is something to implement rather than read.

**Treat every part number here as a category, not a recommendation.** They are
written from memory, this market churns, and models are discontinued and revised
constantly. Verify current availability and specifications before spending
anything.

---

## Telling two vehicles apart by their LEDs

**What it would unlock.** Two robots driven from one console are currently told
apart by battery percentage and by what their cameras see. Colouring each one's
armour LEDs would make identification physical and instant.

**Status: unresolved. The message is understood; the robot ignores it.**

### What is established

`KeyRobomasterSystemLEDColor` is writable, carries no decoded type upstream, and
is never written anywhere in `brunoga/robomaster`. Its payload is JSON, parsed
natively by `json_dto`, and the schema is now known:

```json
{"deviceID":0,"controlMode":0,"R":255,"G":0,"B":0,
 "flashMode":0,"loopCount":0,"time1":0,"time2":0}
```

The channels are **uppercase** `R`, `G`, `B`. Every lowercase guess fails, and
`strings` cannot reveal them — it defaults to a four-character minimum, so they
had to be read from the binary's raw bytes. DJI's own library names the struct
`RMLEDColorMsg`, under `RMSystemParamLEDColor`.

`KeyRobomasterSystemLEDLightEffect` is a different thing entirely:
`{EffectID, Percent, EffectEnable}` — it plays *named preset* effects, not a
colour.

### What was tried, and what happened

A correctly-shaped message is accepted and **changes nothing**. Verified with
the two robots facing each other, reading one's LEDs from the other's camera
and sampling the pixels, across every combination of `deviceID` (0–7, 255),
`controlMode` (0–2) and `flashMode` (0–2), plus the effect key. The LEDs stayed
their default teal in all of them.

### The most likely reason we cannot see the answer

**The send path discards the robot's reply.** These are fire-and-forget events
with no callback registered, so if the robot is rejecting the command — wrong
mode, missing permission, some state the DJI app sets that we do not — the
rejection is invisible. Reading that response is the obvious next step, and it
needs care: `unitybridge.SetKeyValue` **panics** on a key with no decoded type,
because `key.ResultValue()` panics rather than returning an error.

### The cost of getting this wrong, which is unusually high

A malformed payload throws an **uncaught C++ exception inside DJI's library**.
That aborts the process — Go cannot recover — and the robot then refuses new
connections for roughly a minute afterwards. Each wrong guess costs a restart
and takes a vehicle out of service, which is why the field names were read from
the binary rather than discovered one crash at a time.

`internal/leds` holds what is known. Both endpoints stay behind
`-led-experiment`: a control that silently does nothing should not look like a
feature, and one that can abort the vehicle process should not be reachable by
default.

## What the S1's own vision reports — unprobed, and now cheap to ask

Hunting for LED fields turned up a neighbouring block that looks like the
onboard detector's output:

```
Rects · RectX · RectY · RectW · RectH · Color · Distance · Pitch · Yaw · Roll
```

**`Distance` is the interesting word**, and it is also the one to be careful
about. A field-name table carries no struct boundaries, so adjacency does not
prove these belong to one message — the first `Distance` in the binary turned
out to be part of `RMVisionParamTrackingDistance`, which is *write* access: a
setting, not a reading. This is a lead, not a finding.

If it is real and readable, it bears on the depth question DECISIONS.md #15
names as the blocker for automated movement triggers, possibly with no bolt-on
hardware at all. That is worth an hour of somebody's time.

`cmd/s1tof` already knows how to ask: `KeyVisionDebugRect`,
`KeyVisionDetectionEnable` and the running-status keys are all readable, and the
probe reads undecoded keys without the decode that would otherwise panic. The
keys are listed in the command; nobody has run it against them yet.

## Smaller threads

**Decode the position and vision keys.** The self-contained reverse-engineering
project named above. It would benefit every user of this library, not just this
repo.

**`SO_REUSEPORT` upstream.** `support/finder/listener.go` sets `SO_REUSEADDR`,
which is not sufficient on Darwin for two processes to share the discovery
port. Verified directly; see DECISIONS.md #21. A one-line fix worth sending to
`brunoga/robomaster`.

**Replay a recorded drive through a different model.** Every drive is already
logged with frames, detections, captions and operator input, time-aligned
(#15). Nothing yet re-runs a model over one. This is the point at which a
broker would finally earn its place (#14).

**Vehicles on separate hosts.** The supervisor already speaks HTTP and
WebSocket to its workers; nothing assumes they are local. Untested.

**Something to do with two robots.** Switching between them is built. What is
worth *doing* with two is wide open, and the answer probably arrives from
driving them rather than from planning.
