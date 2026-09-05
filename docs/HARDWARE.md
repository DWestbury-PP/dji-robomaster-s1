# Hardware & Transport Paths

Everything in this repo above the transport layer is vehicle-agnostic. This
document covers the part that isn't: how a laptop gets bytes onto an S1.

## 0. Firmware triage — do this before anything else

The S1's firmware version decides which transports are open. **Check it before
connecting to anything that could update it.**

Procedure, per vehicle:

1. Power on the S1. Do **not** put the phone on the internet.
2. Join the phone to the S1's own Wi-Fi AP (direct mode) with cellular data
   **off**. The S1's AP has no uplink, so the app has no route to fetch a
   firmware image while you look.
3. RoboMaster app → connect → settings → about → note the firmware version.
4. Decline every update prompt. Record the version in STATUS.md.

Then read the table:

| Firmware | Path A (root + EP SDK) | Notes |
|---|---|---|
| ≤ 00.06.0300 | ✅ open | Root, upload EP SDK, done. |
| 00.06.0518 and later | ❌ patched | Root script fails; the sandbox escape is closed. |
| between / unknown | ⚠️ verify | Try the root step; it fails loudly and non-destructively. |

## RESULT (2026-09-04): both vehicles are on 00.06.0518 — Path A is closed

| Vehicle | Active | Downloaded / staged |
|---|---|---|
| 1 | **00.06.0518** | 00.06.0521 |
| 2 | **00.06.0518** | 00.06.0521 |

Both units sit on the first firmware known to break the root hack, and neither
differs from the other, so the "root one, keep one stock" split is moot
(DECISIONS.md #8, superseded). No clean downgrade path is known.

**Standing hardware rule: do not let 00.06.0521 install.** It is staged on both
vehicles, one confirmation tap away. It almost certainly changes nothing for
rooting — both are post-patch — but 00.06.0518 is the better-documented state
and there is no way back. Keep the phone off the internet when using the app.

DJI has wound down the education line and is not shipping new S1 firmware, so
a vehicle that is old enough today stays viable indefinitely. There is no known
clean downgrade path for one that isn't.

**Two vehicles is the luxury here.** If they differ, root the older one (Path A)
and keep the newer one as the Path C testbed. If both are new, Path B is the
fallback and Path C the escape hatch.

## Path A — root + EP SDK (preferred)

The RoboMaster EP is effectively an S1 with extra hardware, and its SDK is
documented by DJI. The community hack roots the S1's Android-based intelligent
controller and uploads the EP SDK files over adb. Afterward the robot speaks
the official protocol:

| Port | Protocol | Carries |
|---|---|---|
| 40923 | TCP, plaintext lines | commands + responses (`chassis speed x .. y .. z ..;`) |
| 40921 | TCP | H.264 video, 720p30 |
| 40925 | TCP | push / event stream (telemetry) |
| 40926 | UDP | broadcast / discovery |

Gives us: chassis velocity and per-wheel control, gimbal pitch/yaw, blaster,
LEDs, IMU/attitude telemetry, and raw video. Documented, Python-native, and the
protocol is plaintext enough to drive from a socket without DJI's SDK package
if we prefer.

- Hack guide: https://github.com/collabnix/robomaster/blob/main/hack.md
- Official SDK + docs: https://github.com/dji-sdk/RoboMaster-SDK
- Protocol reference: https://robomaster-dev.readthedocs.io/en/latest/

**Risk:** rooting is irreversible-ish and unsupported. Do it on one vehicle
first, keep the second stock until the first is proven.

## Path B — app-mode impersonation (**selected**)

`brunoga/robomaster` (Go) drives a **stock, unrooted** S1 by wrapping DJI's
proprietary UnityBridge native library. It impersonates the mobile app, so it is
firmware-independent — its only precondition is that the app still works on the
vehicle, which we have now confirmed on the bench.

Verified against the repo (2026-09-04):

| Question | Finding |
|---|---|
| Maintained? | Yes — last push 2025-05-18. One maintainer, ~56 stars. |
| Needs root? | No. Stock vehicle, current firmware. |
| Apple Silicon? | Yes, with a caveat — see below. |
| Blob sourcing? | **Vendored in-repo** (`unitybridge/wrapper/lib/`) for android arm/arm64, darwin amd64, ios arm64, windows. No extraction from the DJI app needed. |
| Control surface | `module/{chassis,gimbal,gun,camera,controller,connection,robot,gamepad,sdcard}` — everything we need. |
| Video | `module/camera` delivers **decoded RGB frames via callback** (`rgb.go`, `video_callback.go`). No H.264 parsing on our side. |

**The Apple Silicon caveat.** There is no arm64 macOS build of DJI's library.
`install.go` is explicit: *"MacOS arm64 will use the amd64 library through
Rosetta"*, and `compile_darwin_arm64.sh` builds the Go binary as
`GOARCH=amd64`. So the driver process runs under Rosetta 2 on the M-series Mac.
Acceptable for an I/O-bound process; the thing to actually measure is video
decode, which happens inside the emulated blob rather than on VideoToolbox
(ARCHITECTURE.md §6). Apple has signalled Rosetta 2 will be pared back after
macOS 27 — a real revisit trigger, mitigated by the fact that the driver is a
small relocatable process (DECISIONS.md #10).

**Cost, stated honestly.** The API is a reverse-engineered key/value + event
system, not a clean SDK. The repo's own README says the remaining work is
"figuring out what each key does and when they should be used." Budget for that.

Related repos by the same author, both useful later:

- https://github.com/brunoga/robomaster — the library
- https://github.com/brunoga/robomaster-control — sample control program
- https://github.com/brunoga/robomaster-mobile — gomobile interface; **the path to the eventual native mobile app talking to the S1 directly**
- https://github.com/brunoga/robomaster-tracker — GoCV ball tracker, reference for a vision loop

Note: the vendored DJI library is proprietary. Personal use, private repo; we
neither redistribute it nor commit it here.

## Path C — brain transplant via CAN bus (escape hatch, second vehicle)

Bypass the DJI intelligent controller entirely: put a Pi 4 + STM32 on the S1's
internal CAN bus and command motors, gimbal and blaster directly. Firmware
proof, unlimited authority, and a genuine hardware project — CAN transceiver,
harness, soldering, ROS1-era reference code.

**Open question before committing:** the camera and Wi-Fi appear to live with
the controller being replaced, which would mean sourcing our own camera and
link. That has to be verified on the bench, not assumed — and it matters a lot,
because the whole VLA premise runs on that camera.

- https://github.com/RoboMasterS1Challenge/robomaster_s1_can_hack

## What we borrow, and what we don't

**Borrow:** the wire protocol, the root toolchain, and the reverse-engineering.
**Don't borrow:** architecture. The S1 repos are hobby-grade, mostly 2020–21,
ROS1, and unmaintained. We take their protocol knowledge and write our own
stack above it.
