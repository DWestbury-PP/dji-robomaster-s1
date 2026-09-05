# M1 field runbook — self-contained, works with no internet

This card assumes you are on the S1's Wi-Fi with no route to the outside world
and no Claude session. Everything needed is already on disk.

## Pre-flight: already done, verified 2026-09-04

| Thing | State |
|---|---|
| DJI bridge at `~/.unitybridge/unitybridge` | ✅ installed, 15 MB |
| `bin/s1probe` | ✅ built, `Mach-O x86_64` |
| Go module cache | ✅ populated — `GOPROXY=off go build` succeeds |
| Bridge loads under Rosetta | ✅ verified with no robot present |

**Nothing needs the internet.** If you edit code and rebuild offline:

```bash
GOPROXY=off ./scripts/build.sh
```

## Option A — keep your internet (recommended)

Your Mac's `Ethernet` service is **ordered ahead of Wi-Fi**, so with a wired
adapter plugged in, internet flows over Ethernet while Wi-Fi joins the S1's AP.
You keep the session and get direct mode. Your LAN is `192.168.1.0/24` and the
S1's AP is typically `192.168.2.0/24`, so there is no subnet collision.

Verify after plugging in and joining the S1:

```bash
route -n get default | grep interface   # want: en0 (Ethernet)
ifconfig en1 | grep "inet "             # Wi-Fi should hold an S1-side address
```

USB tethering from an iPhone works the same way, but check the service order —
Ethernet is already first, so it is the cleaner option.

## Option B — router mode, no Wi-Fi switching at all

Use the RoboMaster app to put the S1 into **router/station mode** joined to your
own network. Then the Mac never leaves the LAN and you drop `-wifi-direct`:

```bash
./bin/s1probe -connect-only
```

⚠️ This means opening the DJI app. **Decline every firmware prompt** —
00.06.0521 is staged on both vehicles and must not install (HARDWARE.md).

## Option C — go dark

Join the Mac to the S1's AP and work from this card.

## The runs

```bash
# 1. Does it connect at all? ~30 s, no motion.
./bin/s1probe -wifi-direct -connect-only

# 2. Full run. Gimbal moves in place; nothing drives.
./bin/s1probe -wifi-direct -video 60s -json runs/direct-01.json

# 3. Repeat while carrying/moving the robot — jitter under motion is the
#    number that matters most.
./bin/s1probe -wifi-direct -video 60s -json runs/direct-moving.json

# 4. Router mode, for comparison, if you set up Option B.
./bin/s1probe -video 60s -json runs/router-01.json
```

Optional, only with the vehicle on the floor and clear space:

```bash
./bin/s1probe -wifi-direct -video 30s -allow-chassis -json runs/direct-chassis.json
```

## Reading the result without me

| What you see | What it means |
|---|---|
| FPS near 30, CPU well under 1 core | Rosetta is a non-issue. Proceed to M2. |
| FPS sagging, CPU near/above 1 core | Decode is losing to emulation. Re-open SPIKE-arm64-bridge.md. |
| Healthy FPS, high `video_inter_frame` jitter | The link, not the CPU. Compare Wi-Fi modes. |
| `starting client: timeout` | Not connected. See below. |

## Troubleshooting, offline

**`starting client: timeout`**
- Is the vehicle powered on, and did its lights finish booting?
- In direct mode: is the Mac actually joined to the S1's AP?
  `ifconfig en1 | grep "inet "` — you should *not* see `192.168.1.x`.
- In router mode: the S1 must be configured to join your LAN first, and needs a
  matching appID. `-appid 0` connects to the first robot found.
- Your WireGuard VPN (`darrell`) was **disconnected** when checked. If you turn
  it on, a default-route VPN can swallow traffic to the robot. Leave it off.

**`no frames received` but connect succeeded**
- Control plane works, video does not. Note it and move on — that is a real
  finding, and an important one.

**Bridge fails to load**
- `ls -l ~/.unitybridge/unitybridge` should show ~15 MB.
- Re-run `./scripts/install-bridge.sh` (works offline; reads the module cache).

**It hangs**
- Ctrl-C. The deferred report still prints what it collected, and partial
  results are marked `"incomplete": true` in the JSON.

## Bring back

- Everything in `runs/*.json` (gitignored, so they persist locally).
- Terminal output — the printed table has the same numbers in readable form.
- Rough notes: distance from robot, walls in between, whether it was moving,
  battery percentage.

With those I fill in ARCHITECTURE.md §6 and call the go/no-go on Rosetta (#11).
