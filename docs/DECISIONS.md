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

### 6. Safety is a library inside `s1-link`, not a service

**Decision.** Deadman, clamps, arming and e-stop are enforced in-process on the
last hop before the wire.

**Why.** A safety *service* is a hop, and a hop is something a future consumer
can be written around — including by us, at 1 a.m., "just to test something."
Inside `s1-link` there is no other path to the robot, so the guarantee holds by
construction rather than by discipline.

**Revisit if.** Never, ideally. If a second process ever needs to talk to the
robot, it goes through `s1-link`.

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

### 8. Root one vehicle, keep one stock

**Decision.** Whichever S1 can take Path A gets rooted; the second stays
untouched until the first is proven end-to-end.

**Why.** Rooting is unsupported and the community reports firmware updates
breaking working installs. Two vehicles means the experiment has a control. If
their firmware versions differ, the split is free: root the older one, and the
newer one becomes the Path C (CAN bus) testbed it was always going to be.
