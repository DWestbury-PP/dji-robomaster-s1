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

### Variants

`coupon` (5 mm) · `h15` · `h20` · `h25`. Heights include a 4 mm collar around
the bore that keeps the gimbal loom off the printed edge.
