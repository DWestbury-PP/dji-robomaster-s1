# cad

Parametric geometry for bolt-on hardware, built in Blender from measured
numbers. The scripts are the source of truth; `.blend` files and `.stl` exports
are outputs and are gitignored.

## Why scripts rather than a modelled file

Every dimension here came from a caliper or a manual, and several will change
as more get measured. A named constant at the top of a script is a one-line
edit that regenerates the part; a hand-modelled mesh is a remodel. It also
means the provenance of each number — which ones are measured, which are still
estimates — lives next to the number itself.

## Running

Blender must be open with the `blender-mcp` addon started (it listens on
`127.0.0.1:9876`). No MCP server is needed; the addon's socket takes JSON
directly:

```python
import socket, json
s = socket.create_connection(("127.0.0.1", 9876))
s.sendall(json.dumps({"type": "execute_code",
                      "params": {"code": open("cad/s1_riser.py").read()}}).encode())
print(json.loads(s.recv(1 << 20))["result"]["result"])
```

STLs land in `3D-models/print/` (gitignored, as is everything under
`3D-models/`).

## s1_model.py — the calibrated S1 reference

Rebuilds the reference robot from the source FBX: strips the backdrop
billboard the model ships with, aligns it to world axes, scales it on the
measured 235 mm track width, and sits it on the floor at the origin.

Idempotent — run it twice and you get one robot, not two.

**This is why no `.blend` needs versioning.** The valuable content isn't the
mesh, it's the calibration; with that in a script, any stage is a script run
rather than a binary someone has to keep.

It self-checks on rebuild against two measurements the calibration never saw:

| | Model | Measured |
|---|---|---|
| Ground clearance | 29.89 mm | 30.0 |
| Rear protrusion | 27.77 mm | 27.0 (battery latch) |
| Length | 297.7 mm | 315.0 — **~5.5% short, do not trust X** |

Measuring that clearance needs care: a box around the centre clips the edge of
a wheel well and reports the wheel. An earlier hand-tuned box did exactly that
and gave a right answer for the wrong reason. The script instead excludes whole
objects that touch the floor, since the chassis underside cannot be one.

## s1_riser.py — gimbal riser

Lifts the gimbal to make room for a sensor array, without modifying the robot.

```
chassis  ->  4x M4 male-female standoff (metal)  ->  gimbal
             printed plate captured, carrying no clamp load
```

The standoff's male stud threads into the chassis where DJI's screw went; its
female end takes that same M4x8 back. No interface on the robot is adapted,
and the whole thing reverses with a screwdriver.

The plate is kept out of the clamp path on purpose — printed plastic creeping
under preload would show up as camera shake, and this project tracks video
stability (30.1 fps, 3.3 ms sigma), so that regression would be real and hard
to attribute after the fact.

### Measured vs estimated

| Constant | Value | Source |
|---|---|---|
| `BOLT_X` / `BOLT_Y` | 58 / 77 mm | calipers, hole centres |
| `ORIG_SCREW_L` | 8 mm | calipers: 9 mm overall less a 1 mm head |
| M4 | — | DJI Quick Start Guide v1.4, step 59 (`4x M4-B`) |
| `MOUNT_Z` | 108 mm | calibrated model, Z axis validated to 0.8% |
| **`BORE_D`** | **46 mm** | **ESTIMATE from a photo — not measured** |
| **`CHASSIS_THREAD`** | **5 mm** | **ESTIMATE — a stud that bottoms out holds nothing** |

The two estimates are the open risks. `CHASSIS_THREAD` is the dangerous one:
too long a stud feels tight while clamping nothing, holding a gimbal.

### Printing (Bambu X1C, 0.4 mm nozzle)

Print **flat on the bed as modelled** — every hole is then vertical and needs
no support. 0.2 mm layers, 3–4 walls.

Start with **`coupon`** (5 mm, ~10 min). It has the full bolt pattern and bore
but none of the height, so it checks the two things most likely to be wrong —
hole positions and bore clearance — before committing to a tall print.

The hex pockets are sized to **capture** the standoff so it cannot spin while
the top screw is torqued. Vertical holes print 0.1–0.2 mm undersize on an X1C,
so `HEX_CLEAR` is the constant to tune from the coupon: too loose and the
capture is lost, too tight and it will not seat.

PLA is adequate — the plate is not load-bearing. PETG or ASA if you want heat
margin in a car or a sunny room.

### Design stages

`STAGES` holds a complete parameter set per stage, and `BUILD` picks which to
export. Reverting to an earlier idea is an edit to `BUILD`, not an archived
binary — geometry is cheap to regenerate, the decision behind it is not, so a
`note` travels with each set of numbers.

| Stage | Height | For |
|---|---|---|
| `fit-coupon` | 5 mm | Pattern and bore only, ~10 min. Print this first. |
| `h15` | 15 mm | Least lift that still leaves volume; shortest lever arm. |
| `h20` | 20 mm | Middle. |
| `h25` | 25 mm | Most volume, most lever arm. |

Heights above the coupon include a 4 mm collar around the bore, keeping the
gimbal loom off a printed edge.

`SUPERSEDED` records fastening schemes that were tried and rejected. No
geometry is kept — they failed on interface grounds, not dimensions — but the
reasons are what stop them being re-proposed later:

- **`long-screw`** — one long M4 per corner through the whole stack. DJI's
  M4-B has a **1 mm head**; catalogue low heads start near 2.2 mm, so that
  screw isn't a purchasable part.
- **`counterbore-insert`** — riser bolts down with counterbored screws, gimbal
  bolts up into heat-set inserts. Both sets share the 58 × 77 centres, so the
  insert pocket and the lower screw's driver access want the same space.
