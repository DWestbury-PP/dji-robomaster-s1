# Status

> The living progress log. Read this + [ARCHITECTURE.md](ARCHITECTURE.md)
> to resume work. Rationale: [DECISIONS.md](DECISIONS.md). Transport paths and
> firmware triage: [HARDWARE.md](HARDWARE.md). Current milestone: [M1.md](M1.md).
> Investigations:
> [SPIKE-arm64-bridge.md](SPIKE-arm64-bridge.md).

## Where things stand (2026-09-04, end of session 1)

**Design only. No code written.** M0 is complete: both vehicles were powered up
and their firmware read, which settled the transport.

## M0 result — Path A is closed, Path B is selected

| Vehicle | Active firmware | Staged |
|---|---|---|
| 1 | **00.06.0518** | 00.06.0521 |
| 2 | **00.06.0518** | 00.06.0521 |

00.06.0518 is the version that closes the root exploit, and both units are on
it, so there is no version split to exploit and no known downgrade. **We drive a
stock, unmodified S1 through the app-mode UnityBridge library**
([brunoga/robomaster](https://github.com/brunoga/robomaster)) — HARDWARE.md
Path B, verified this session as maintained, Apple-Silicon-capable via Rosetta,
with the vendored DJI blobs in-repo and a full control + camera surface.

⚠️ **Standing hardware rule: do not let 00.06.0521 install on either vehicle.**
It is staged and one confirmation tap away. Keep the phone off the internet when
using the RoboMaster app.

## What M0 changed in the design

- `s1-link` and `s1-video` **collapsed into one Go process, `s1-driver`** — the
  bridge handle is process-wide, so control and video must share a process
  (DECISIONS.md #9).
- The safety layer is now **written in Go**, still on the last hop (#6).
- Go enters the stack, running as amd64 under Rosetta 2, with a documented exit
  to a Linux host if Rosetta goes away (#10).
- Decision #8 (root one, keep one stock) is **superseded**. Vehicle 2 stays
  pristine as the control and as the future Path C testbed.
- No H.264 parsing needed — the camera module delivers decoded RGB via callback.

## Roadmap

See ARCHITECTURE.md §7.

| Milestone | State |
|---|---|
| M0 — firmware triage, transport choice | ✅ done — Path B |
| M1 — link + latency harness | 🔨 software half **done and verified**; needs a powered-on S1 |
| arm64 bridge spike | investigated, **deferred** (DECISIONS.md #11) |
| M2 — safety layer (Go) | not started |
| M3 — browser teleop | not started |
| M4 — video into foveate | not started |
| M5–M7 — intentions, autonomy, mobile | not started |

## M1 in progress

`s1probe` is written, builds as amd64, and was smoke-tested with no robot
present. The meaningful result from that: **DJI's UnityBridge loads,
initializes, runs its event loop and scans for robots under Rosetta 2** on
Apple Silicon. The transport is real, not theoretical. Idle CPU was 0.01 cores.

What remains is hardware-in-the-loop — see [M1.md](M1.md) for the procedure:

- [ ] `./bin/s1probe -wifi-direct -connect-only` against a powered-on vehicle
- [ ] Full run in WiFi Direct mode
- [ ] Full run in router mode, for comparison (open question #3)
- [ ] Fill in ARCHITECTURE.md §6 and decide go/no-go on Rosetta decode (#11)

## Open questions

1. **Key discovery.** The bridge is a reverse-engineered key/value + event API;
   upstream's README says the remaining work is learning what each key does. M1
   should produce a map of the keys we actually need.
2. **Rosetta decode cost.** Video decode happens inside the emulated blob, not
   on VideoToolbox. Measure in M1; it is one of two numbers that could kill the
   design (ARCHITECTURE.md §6).
3. **Wi-Fi mode.** Direct (S1 as AP) may force the Mac off the network hosting
   Redis and Ollama. Router mode avoids that but adds a hop. Measure both.
4. **Two vehicles at once.** The bridge handle is process-wide; whether a second
   S1 can be driven from the same host is unknown.
5. **Path C camera.** If we ever replace the intelligent controller, does the FPV
   camera and Wi-Fi go with it? Assumed yes, unverified. Bench question.
6. **foveate M8.** Multi-camera + crash recovery is the prerequisite for M4 here.
   Sequence it in the foveate session, not this one.
7. ~~**Battery health.**~~ **Resolved 2026-09-04** — two batteries hold a full
   charge. The short session-1 runtime was an impatient partial charge, not
   degradation. No blocker for M1.

## Session log

**Session 1 (2026-09-04).** Surveyed the S1 landscape: EP SDK hack (Path A),
app-mode impersonation via UnityBridge (Path B), CAN bus brain transplant
(Path C) — see HARDWARE.md. Confirmed foveate's roadmap already anticipates this
consumer (M10 `intentions`, "schema TBD with the movement system"). Wrote the
initial design set. Operator then powered up a vehicle and read firmware on
both: 00.06.0518 active, 00.06.0521 staged, killing Path A. Verified Path B
against the upstream repo and revised the design around a single Go
`s1-driver`. Ran a 20-minute spike on native-arm64 feasibility — reached a hard
dyld process-platform wall, documented and deferred (#11). Batteries confirmed
healthy. No code.
