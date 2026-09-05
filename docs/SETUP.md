# The working setup

Everything about *this environment* — the machines, the network, the toolchain
and the traps — in one place. Verified 2026-09-04.

For the field procedure with the robot in front of you, use
[M1-RUNBOOK.md](M1-RUNBOOK.md). This document is the reference; that one is the
card you read when the Mac has no internet.

---

## 1. Hardware

### The vehicles

Two DJI RoboMaster S1s, shelf-retired for ~2 years, now the movement platform.

| | Vehicle 1 | Vehicle 2 |
|---|---|---|
| Active firmware | **00.06.0518** | **00.06.0518** |
| Staged firmware | 00.06.0521 | 00.06.0521 |
| State | in use, unmodified | pristine, untouched |

⚠️ **Standing rule: never let 00.06.0521 install.** It is staged on both units,
one confirmation tap away in the DJI app, and there is no way back. Keep the
phone off the internet whenever the app is open.

00.06.0518 is the firmware that closed the root exploit, so **Path A (root + EP
SDK) is unavailable on both** and we drive them stock — see
[HARDWARE.md](HARDWARE.md).

Batteries are healthy: two hold a full charge, and a 45 s motion run with the
drive motors loaded costs about 1%. Not a constraint. DJI no longer sells
replacements, so treat them as irreplaceable.

Devices the robot enumerates on connect, all present:

```
ImageTransmission  Camera  Chassis  Battery  Gimbal  WaterGun
ESC0 ESC1 ESC2 ESC3
FrontArmor BackArmor LeftArmor RightArmor LeftHeadArmor RightHeadArmor
```

### The host

| | |
|---|---|
| Machine | Mac mini (Mac16,11), **Apple M4 Pro**, 14 cores (10P/4E), 64 GB |
| OS | macOS 26.6.2 (Darwin 25.6.0), arm64 |
| Rosetta 2 | present — **required**, see §3 |

---

## 2. Network — router mode (current), and the direct-mode past

**The S1 is joined to the house network.** One network for everything; no
adapter, no interface juggling.

| | |
|---|---|
| Robot | `192.168.1.39`, MAC `60:60:1f:cd:b8:66` |
| App ID | `4050813280395343415`, paired |
| Announces | UDP broadcast on `:45678`, ~every 500 ms |
| Mac | ordinary Wi-Fi, single default route |

Find it any time — this works when `ping` does not, because the S1 ignores ICMP:

```bash
./bin/s1find            # one-shot: is the robot on the network, and paired?
./bin/s1find -watch     # every broadcast packet, to confirm it is still alive
```

Router mode is not just more convenient. It is **measurably better**: jitter
~7× lower and the p99 tail 3× lower than the robot's own AP
(ARCHITECTURE.md §6). It is the default for both binaries; `-wifi-direct` opts
back out.

Requirements, if it ever has to be reconfigured: the DJI app (⚠️ **decline the
staged 00.06.0521**), a WPA/WPA2-PSK network — WPA3 is a likely failure — with
client isolation off, and a QR code held up to the robot's camera.

### The direct-mode arrangement, kept for reference

In WiFi Direct the robot serves its own AP with no uplink, so joining it costs
all internet access. A USB CAT5 adapter solved that: **Ethernet carries the
internet, Wi-Fi carries the robot.**

| Interface | Service (order) | Role |
|---|---|---|
| `en9` | USB 10/100/1000 LAN (#2) | **default route** — internet, and this session |
| `en1` | Wi-Fi (#4) | the robot's AP only |
| `en0` | Ethernet (#1) | built-in port, unused/inactive |

Driving the robot puts `en1` on `192.168.2.x` with the S1 at `192.168.2.1`,
while `en9` stays on the house LAN at `192.168.1.0/24`. Different subnets,
different gateways, no ambiguity. The S1 does not answer ICMP, so `ping` is not
a connectivity test — check the ARP table, or just try connecting.

**Router mode would remove all of this.** The S1 can join an existing network
instead ("Connection via Router" in the DJI app), which DJI says gives broader
coverage. One network for Redis, Ollama and the robot; no adapter, no toggling,
no same-subnet trap. Not yet done — see STATUS.md open question #3 for the
requirements and the caveats.

Both connection modes tolerate this:

- **WiFi Direct** does no discovery — the bridge dials the S1's fixed AP address
  and the OS routes it over the interface owning that subnet.
- **Router mode**'s finder binds UDP `:45678` on the wildcard address, so it
  receives the robot's broadcasts on *any* interface.

### ⚠️ The trap, if you ever go back to direct mode: never leave Wi-Fi on the house LAN

If Wi-Fi rejoins the house network while Ethernet is plugged in, **both
interfaces land on `192.168.1.0/24` behind the same gateway** and macOS installs
two default routes:

```
default   192.168.1.1   UGScg    en9
default   192.168.1.1   UGScIg   en1
```

A long-lived TCP connection is pinned to a source address. macOS re-elects the
primary service on any link event — Wi-Fi roam, DHCP renew, sleep/wake, adapter
renegotiation — and each flip changes the source address and kills every
established connection. This showed up as **intermittent, self-healing API
disconnections**, and it is absent while driving the robot because that uses a
different subnet.

```bash
netstat -rn -f inet | awk '$1=="default"'   # more than one line = this bug

networksetup -setairportpower en1 off       # between sessions
networksetup -setairportpower en1 on        # to join the robot
```

Durable fix: turn **off Auto-Join for the house SSID** (System Settings → Wi-Fi
→ Details) so Wi-Fi only ever connects when deliberately pointed at the robot.

Also: the WireGuard VPN (`darrell`) must stay **disconnected** during robot work.
A default-route VPN swallows traffic to the S1.

---

## 3. Toolchain

| | |
|---|---|
| Go | **1.27.1**, `/opt/homebrew/bin/go` (Homebrew 6.0.21) |
| clang | 21.0.0, Command Line Tools at `/Library/Developer/CommandLineTools` |
| Bridge | `github.com/brunoga/robomaster` **v0.0.11** |
| Blob | `~/.unitybridge/unitybridge`, 15,971,232 bytes, Mach-O x86_64 |

### Why everything builds as amd64

DJI never shipped an arm64 macOS build of the UnityBridge and never will, so
`s1probe` is built `GOARCH=amd64` and runs under **Rosetta 2**. This is not a
performance problem — measured at 30.1 fps of 720p video for 0.20–0.25 cores
(ARCHITECTURE.md §6).

The native-arm64 route is understood and deliberately deferred: DJI's *iOS*
build is arm64 and unencrypted and re-platforms to Mac Catalyst in one `vtool`
command, but dyld requires the loading process to match platform and neither Go
nor CLT clang can emit a Catalyst binary. See
[SPIKE-arm64-bridge.md](SPIKE-arm64-bridge.md) and DECISIONS.md #10, #11.

Rosetta is on a clock — Apple has signalled it will be pared back after macOS 27.
The exit is to relocate `s1-driver` to a Linux amd64 host; the bus makes that
packaging rather than a rewrite.

### First-time setup

```bash
brew install go
./scripts/install-bridge.sh   # copies DJI's blob from the Go module cache to ~/.unitybridge
./scripts/build.sh            # builds bin/s1probe as amd64
```

`install-bridge.sh` reads the module cache, so it works offline once
`go mod download` has run. The blob is proprietary DJI code: **never committed
here, never redistributed.**

Everything rebuilds with no network:

```bash
GOPROXY=off ./scripts/build.sh
```

---

## 4. This repo

```
cmd/s1teleop/      the browser console: bridge + governor + loop + HTTP (M3)
cmd/s1probe/       the measurement harness: motion program, safety demo, report
cmd/s1find/        broadcast discovery: is the robot on the network, and paired?
cmd/s1narrate/     the observer: pulls frames, narrates them in prose
cmd/s1capture/     builds a frame corpus from a drive, for model comparison
cmd/s1bakeoff/     scores candidate models on a corpus, local and hosted
internal/safety/   the governor — deadman, clamps, arming, e-stop (M2)
internal/driver/   the 20 Hz control loop, behind a hardware-free Sink
internal/teleop/   MJPEG hub, WebSocket, embedded UI
internal/stick/    the one place a DJI StickPosition may be built from
internal/probe/    distribution stats + the JSON report shape
scripts/           install-bridge.sh, build.sh
runs/              measurement JSON (gitignored)
docs/              this file, and the ones below
```

Six binaries, in two groups.

**Running the robot:** `s1teleop` is the real thing — it holds the bridge, runs
the governor and the control loop, and serves the console. `s1narrate` is the
observer, a separate process so a slow model can never affect driving.
`s1find` is the router-mode diagnostic.

**Measuring it:** `s1probe` produced ARCHITECTURE.md §6 and carries the hardware
safety demo. `s1capture` builds a frame corpus from a drive and `s1bakeoff`
scores models against it — together they are why model choice is settled by
evidence rather than by reading spec sheets.

| Doc | What it is |
|---|---|
| [STATUS.md](STATUS.md) | **Start here.** Where things stand, open questions, session log |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Services, command model, safety rules, measured latency budget |
| [DECISIONS.md](DECISIONS.md) | Every non-obvious choice, with revisit triggers |
| [HARDWARE.md](HARDWARE.md) | The three transport paths and the firmware situation |
| [M1.md](M1.md) / [M1-RUNBOOK.md](M1-RUNBOOK.md) | The latency milestone, and its offline field card |
| [M3.md](M3.md) | The browser console: running it, controls, what it measures |
| [SPIKE-arm64-bridge.md](SPIKE-arm64-bridge.md) | Why we run under Rosetta |

Ignored on purpose: `bin/`, `runs/`, and `log/` — DJI's library writes its own
log directory into the working directory on every run.

---

## 5. Peer repos

**`foveate`** — `~/Documents/Source Code/foveate`, the perception stack. It owns
the tiers; this repo owns the vehicle. They meet on a Redis Streams bus, where
`s1-driver` will publish foveate's own `FrameMessage` on the `frames` stream so
the robot's camera enters the existing pipeline as just another camera.

State as of 2026-09-04: on `main` at `efee82c`. The `frames` contract is intact
in `services/shared/schemas.py`. A recent commit moved 3D-printing work *out* to
a `blender-hand` project — "Foveate returns to being the vision system" — so the
seam is unaffected. **foveate has not yet landed its M8 (multi-camera), which is
the prerequisite for M4 here.** Sequence that work in the foveate session.

Foveate's own dependencies: Docker Desktop (Redis), Ollama with a vision model.
At the time of writing **Ollama is reachable; Docker is not running**, so the
bus is down — fine, because nothing in this repo needs it until M4.

---

## 6. Everyday commands

```bash
# build all three binaries
./scripts/build.sh

# where is the robot?
./bin/s1find

# tests
go test -race ./...
go test -cover ./internal/safety/ ./internal/driver/ ./internal/teleop/

# drive it — router mode is the default, no network juggling
./bin/s1teleop                      # console at http://localhost:8700
./bin/s1teleop -mock                # synthetic video, no robot needed
./bin/s1teleop -fps 10 -quality 60  # if the stream struggles
./bin/s1teleop -speed-level medium  # robot-side gear; slow (default), medium, fast

# narrate what it sees — separate process, start and stop it freely
./bin/s1narrate -v                        # gemma4:e4b every 20s
./bin/s1narrate -model qwen2.5vl:7b -interval 12s -v

# compare models on a recorded drive
./bin/s1capture -out corpus/drive-02 -count 250 -note "kitchen and hallway"
./bin/s1bakeoff -corpus corpus/drive-02 -models "gemma4:e4b,qwen3-vl:8b" -think off

# measure it
./bin/s1probe -connect-only
./bin/s1probe -video 60s -json runs/$(date +%Y%m%d-%H%M).json
./bin/s1probe -motion -video 45s      # drives the robot
./bin/s1probe -safety-demo            # proves the safety layer
```

Open the console in a **focused window**. Chrome throttles timers in hidden
tabs, so a backgrounded console stops feeding commands and the deadman stops the
vehicle — correct, but it looks like a fault if you do not know.

---

## 7. Traps that already cost us time

1. **`Chassis.SetSpeed()` silently does nothing.** Movement goes through
   `Controller.Move` — the virtual stick — refreshed at ~20 Hz. A whole motion
   run was recorded against a stationary chassis before this was caught
   (DECISIONS.md #12).
2. **No command through this bridge is ever acknowledged.** `DirectSendKeyValue`
   is fire-and-forget: a `nil` error is *not* evidence the robot acted. Watch the
   robot, or read telemetry.
3. **Speeds are stick deflection in [-1, 1], not m/s.** The safety clamp is a
   deflection clamp.
4. **The vehicle watchdogs the virtual stick.** A single send does nothing, and
   motion stops when the stream lapses — the hardware enforcing our own
   deadman design.
5. **Control-plane RTT is not measurable.** Even `useCache=false` returns in
   0.2 ms because the bridge answers from its own state.
6. **`BatteryPowerPercent()` panics** if called before the robot's first battery
   push — it dereferences a nil atomic pointer. Guard it.
7. **Two NICs on one subnet breaks long-lived connections.** §2.
8. **`controller.StickPosition` negates Y but not X.** Passing our convention
   (positive = forward/up) in raw inverts drive and turret pitch while leaving
   strafe and yaw correct — which is what makes it confusing rather than
   obvious. Build stick positions only through `internal/stick.ToVirtual`.
9. **A backgrounded browser tab stops the robot.** Chrome throttles hidden-tab
   timers to ~1 Hz, so the deadman fires. Working as intended.
10. **The S1 ignores ICMP.** `ping` failing proves nothing about whether the
    robot is up. Use `bin/s1find`, which listens for its broadcast.
11. **Power-cycling the robot silently disables movement.** Movement control is
    per-robot-session state; the bridge transparently restores video and
    telemetry on reconnect, so everything looks healthy and nothing moves.
    `s1teleop` now re-applies session setup on reconnect — but any new
    per-session state must be re-applied there too.
12. **The bottom of the frame is not UI space.** A camera 20 cm off the floor
    makes the lower third the *most* navigationally important region, inverting
    the usual convention. Any overlay must justify what it occludes at floor
    level (DECISIONS.md #19).
13. **Ollama and the Anthropic API disagree on schemas.** The API requires
    `additionalProperties: false` on every object; Ollama does not care. And
    Ollama streams `message.thinking` separately from `message.content`, both
    drawn from one token budget — miss it and reasoning models look broken.
