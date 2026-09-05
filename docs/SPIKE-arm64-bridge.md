# Spike: can the UnityBridge run natively on Apple Silicon?

**Date:** 2026-09-04 · **Time spent:** ~20 min · **Verdict:** blocked on process
platform; not worth doing before M1. See DECISIONS.md #11.

## The question

`s1-driver` must run as amd64 under Rosetta 2 because DJI never shipped an
arm64 macOS build of the UnityBridge (DECISIONS.md #10). Could we get a native
arm64 build instead — and what would "porting" actually mean?

**First correction: there is nothing to port.** The UnityBridge is a stripped,
closed-source binary. "Port to arm64" would mean *reimplementing* DJI's app-mode
protocol from scratch, not refactoring source. Those are different projects by
two orders of magnitude (see §4).

## 1. What the artifact actually is

| Build | Size | Format |
|---|---|---|
| `darwin/amd64` | 15 MB | Mach-O bundle, x86_64 |
| `android/arm64` | 23 MB | ELF aarch64, stripped |
| `android/arm` | 21 MB | ELF ARM32, stripped |
| `ios/arm64` | 18 MB | **Mach-O dylib, arm64** |
| `windows/amd64` | 14 MB | PE DLL |

17,761 exported symbols, the overwhelming majority a statically-linked OpenSSL.
The real API is **11 C functions**:

```
CreateUnityBridge          UnitySendEvent
DestroyUnityBridge         UnitySendEventWithNumber
UnityBridgeInitialize      UnitySendEventWithString
UnityBridgeUninitialze     UnitySetEventCallback
UnityGetSecurityKeyByKeyChainIndex
UnityPluginLoad            UnityPluginUnload
```

Everything else — chassis, gimbal, gun, camera — is one key/value event protocol
behind `UnitySendEvent*` plus a callback. **All platform builds export the same
11 functions**, verified.

## 2. The promising discovery

**DJI already built arm64** — for iOS. And that binary is:

- `Mach-O 64-bit dynamically linked shared library arm64` — same format and
  architecture as macOS on Apple Silicon.
- **Not FairPlay-encrypted**: `LC_ENCRYPTION_INFO_64` has `cryptid 0`. The
  `SC_Info/` directory alongside it is vestigial.
- Exporting all 11 C entry points, identical to the macOS build.
- Linking 15 system frameworks, **every one of which resolves on this Mac** —
  including the two expected blockers, UIKit and OpenGLES, both present under
  `/System/iOSSupport` (the Mac Catalyst support tree).

Re-platforming the library is a **one-liner**:

```bash
vtool -arch arm64 -set-build-version maccatalyst 13.0 13.0 -replace \
      -output ub_maccat unitybridge && codesign -f -s - ub_maccat
```

That succeeds cleanly — `LC_BUILD_VERSION platform 6`, valid ad-hoc signature.

## 3. Where it actually stops

dyld enforces that the **loading process** matches the library's platform.

Route A — library as Catalyst, loaded from a normal macOS process:

```
DLOPEN FAILED: mach-o file, but incompatible platform
               (have 'macCatalyst', need 'macOS')
```

Route B — library re-platformed to plain `macos`, with UIKit/OpenGLES install
names repointed at `/System/iOSSupport/...` via `install_name_tool`. Fails one
level deeper, on UIKit itself:

```
Library not loaded: /System/iOSSupport/.../UIKit.framework/UIKit
Reason: tried: '.../UIKit' (wrong platform to load into process)
```

**The wall is the host process, not the library.** iOSSupport's UIKit is itself
Catalyst-only, so the process must genuinely be Mac Catalyst. And:

- Go cannot emit Mac Catalyst binaries.
- Command Line Tools clang cannot either — `-target arm64-apple-ios13.0-macabi`
  is rejected; the macabi SDK ships only with full Xcode.

## 4. What it would therefore take

The only viable shape is a **Catalyst-targeted host process** that loads the
bridge and talks to the Go driver over local IPC. Upstream already uses exactly
this pattern on Linux — `wine/dllhost` hosts the Windows DLL and speaks to Go —
so there is a reference for the IPC shape in-repo.

| Step | Estimate |
|---|---|
| Install full Xcode (macabi SDK) | ~1 hr, mostly download |
| Catalyst C/ObjC host: dlopen, expose 11 fns + event callback over a socket | 1–3 days |
| Wire in as a `darwin/arm64` implementation beside `dlopen.go` / `wine.go` | ~1 day |
| **Debug: does the bridge run headless?** | **the real unknown** |

That last row is what decides between three days and three weeks. It is a Unity
*plugin* (`UnityPluginLoad`, `CreateRenderAPI_OpenGLMacOS`) and may expect a run
loop, a UIApplication, or a GL context. It might be fine. It might be a swamp.

**Best case ~3–4 days. Realistic 1–2 weeks. Non-trivial chance of "it will not
run headless," which is unknowable without doing most of the work.**

## 5. The option that is *not* worth it

Reimplementing the protocol — the thing "port" literally implies. Evidence, not
speculation: the same upstream author already tried. `brunoga/robomaster2`,
*"From scratch app mode implementation for the Robomaster S1 and EP"* — 4 stars,
dormant since 2023-11-07, and he went back to shipping the bridge-based library.
The blob statically links a full OpenSSL because there is a crypto/auth
handshake with the vehicle in there to be reversed. **Months, high risk of never
working.** Not a candidate.

## 6. Note on the Jetson

The tempting justification — "we need arm64 for foveate's M9 Jetson profile" —
**does not transfer.** On arm64 Linux the relevant artifact is the *Android*
`arm64` `.so`, which is ELF + bionic, not Mach-O. That is a separate spike with
separate blockers (bionic vs glibc), and upstream's Linux path is Wine + the
x86 Windows DLL, which on a Jetson is emulation again. Worth its own
investigation when M9 is real. It is not this one.

## 7. Recommendation

**Defer until after M1.** We have not measured what Rosetta costs us. The whole
exercise optimizes an unquantified number, and it would *add* a process and an
IPC hop — so the net latency win is not even certainly positive.

Re-open if M1 shows video decode under Rosetta is a material share of the
glass-to-glass budget. At that point this document is the head start: the
library re-platforms in one command, and the only open question is §4's last row.
