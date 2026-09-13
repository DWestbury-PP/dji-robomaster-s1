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

### Tier 2 — "somewhere over there". A shared frame, and this is where a map appears.

An off-screen direction arrow needs both robots in one coordinate system. The
cheap version is not SLAM: it is **tags at measured fixed positions**. Each
robot re-localises exactly when it sees one and interpolates between sightings.
The "map" is a handful of coordinates written on paper.

*Cost:* the tags, measuring them, and the interpolation. Practical indoors;
outdoors it needs posts. *Unverified:* how quickly the estimate becomes useless
between sightings — that is the number this would need to produce.

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

### The architecture that seems right

An **independent pod** — microcontroller, sensors, its own battery, its own
Wi-Fi — that posts to `/perception` like any other producer. It touches no DJI
firmware, which matters on a robot carrying a staged update nobody wants
installed, and it can be built and tested with no robot present.

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

## A bonus find: the vision schema

Hunting for LED fields turned up the neighbouring block, which appears to be the
S1's own vision output:

```
Rects · RectX · RectY · RectW · RectH · Color · Distance · Pitch · Yaw · Roll
```

`Distance` is the interesting word. If that is real and reachable, it bears
directly on the depth question that DECISIONS.md #15 names as the blocker for
automated movement triggers — possibly without any bolt-on sensor at all. The
same binary-reading technique would apply. Nobody has tried.

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
