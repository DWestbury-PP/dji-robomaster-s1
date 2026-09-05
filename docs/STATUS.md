# Status

> The living progress log. Read this + [ARCHITECTURE.md](ARCHITECTURE.md)
> to resume work. Rationale: [DECISIONS.md](DECISIONS.md). Transport paths and
> firmware triage: [HARDWARE.md](HARDWARE.md).

## Where things stand (2026-09-04, end of session 1)

**Design only. No code written.** Session 1 scoped the project, surveyed the S1
hacking landscape, and settled two forks: peer repo on foveate's bus (#1), and
teleop before autonomy (#2).

## Blocker: M0 — firmware triage

Everything about the transport hangs on one unknown: **which firmware the two
vehicles are running.** The design does not depend on the answer — the driver
sits behind an interface (ARCHITECTURE.md §3) — but the first line of code does.

**Action for the operator (see HARDWARE.md §0 for the full procedure):**

- [ ] Vehicle 1 — firmware version: `________`
- [ ] Vehicle 2 — firmware version: `________`

Check with the phone on the S1's own AP and cellular data off, so the app has
no route to fetch an update while you look. **Decline every update prompt.**
≤ 00.06.0300 opens Path A; 00.06.0518+ closes it.

## Roadmap

See ARCHITECTURE.md §7. Nothing started.

| Milestone | State |
|---|---|
| M0 — firmware triage, transport choice | ⛔ blocked on hardware |
| M1 — link + latency harness | not started |
| M2 — safety layer | not started |
| M3 — browser teleop | not started |
| M4 — video into foveate | not started |
| M5–M7 — intentions, autonomy, mobile | not started |

## Open questions

1. **Path C camera.** If we ever replace the intelligent controller, does the
   FPV camera and Wi-Fi go with it? Assumed yes, unverified. Matters a great
   deal — the VLA premise runs on that camera. Bench question, not a web question.
2. **Wi-Fi mode.** Direct (S1 as AP) is simpler and lower-latency; router mode
   puts the robot on the LAN alongside the Mac running foveate and Ollama. M1
   should measure both, since direct mode may force the Mac off its own network.
3. **foveate M8.** Multi-camera + crash recovery is the prerequisite for M4 here.
   Sequence it in the foveate session, not this one.

## Session log

**Session 1 (2026-09-04).** Surveyed the S1 landscape: EP SDK hack (Path A),
app-mode impersonation via UnityBridge (Path B), CAN bus brain transplant
(Path C) — see HARDWARE.md. Confirmed foveate's roadmap already anticipates
this consumer (M10 `intentions`, "schema TBD with the movement system").
Wrote the initial design set. No code.
