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
| `BORE_D` | 46 mm | fit coupon on the chassis, 2026-10-31 — clears the loom |
| `CHASSIS_THREAD` | 5.8 mm | 2026-10-31: the whole stud threads in and seats snug |
| `STANDOFF_AF` | 6.75 mm | calipers, 6.7–6.8 |
| `STANDOFF_BODY` | 20.0 / 24.9 mm | calipers; plate is cut `PLATE_GAP` (0.4) shorter |
| `SENSOR_W/H/T` | 19.9 / 11.6 / 4.0 mm | calipers on a TOF400C, no header |
| `SENSOR_EYE_UP` | 1.5 mm | calipers: optical centre 4.0 from top, 7.0 from pad edge |
| **`SENSOR_CAP_W/H`** | **11.8 / 6.8 mm** | **UNVERIFIED — scaled off a photo** |
| `GIMBAL_DISC_D/H` | 50.3 / 7.8 mm | calipers; H is measured below the arms |
| **`POST_R/W/L`** | **27 / 6 / 6 mm** | **UNVERIFIED — read off a photo; clay impression to follow** |

### The top face: the gimbal disc

The gimbal's base disc is its **electrical interface**: two 4-pin sockets
facing down, which the chassis plugs come up through the bore to meet. Earlier
prints had a 52 mm-OD collar on top that left the disc nowhere to go. The
collar is gone; the top face now has a **51.0 mm counterbore, 7.9 mm deep**.
That is the disc's 7.8 mm, less the 0.4 mm `PLATE_GAP`, plus 0.5 mm below it,
so the disc can never bottom out. If it did, the disc rather than the
standoffs would carry the clamp.

There are also **four notches at 0/90/180/270°** for the small square posts
on the gimbal ring. The posts look as though they reach as low as the disc,
but they haven't been measured, so the notches are deliberately generous.

**Cable slack sets the height.** The chassis plugs reach a 20 mm lift but
not 25, so **h20 is the build**. h25 would need extension leads in DJI's
4-pin format.

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
| `fit-coupon` | 5 mm | Pattern and bore only, ~10 min. Done; all four holes line up. |
| `h15` | 15 mm | Record only. Too short for a sensor, and has no disc counterbore. |
| **`h20`** | **19.6 mm** | **Gimbal-fit test: plain block with the disc counterbore. Print next.** |
| `h25` | 24.5 mm | Record only. The cables lack the slack for it. |
| **`sensor-coupon`** | **19.6 mm** | **One face segment: a `lip` pocket at −X and a `front` pocket at +X.** |
| `h20-sensors` | 19.6 mm | The payload block in `POCKET_STYLE`. Not before both of the above. |

## Sensor coverage, and why the pockets are angled

Eight VL53L1X aimed at 45° spacing, two per face, each pocket splayed
±22.5°.

**Boards lying flat on a face would all aim the same way** — that is
redundancy, not coverage. The obvious fix, chamfering the corners into
diagonal faces, is impossible here: the bolt pattern is 58 × 77 inside a
76 × 95 plate, so *the corners are where the standoffs are*, and every
chamfer wide enough for a board cuts into a hex pocket. Angling the pockets
inside the flat walls buys the arc and changes no outline. Verified
clearance from sensor geometry to the nearest standoff pocket: **5.77 mm**.

Coverage is 216° of 360° (60%), leaving 18° gaps. At 500 mm a gap spans
158 mm and the robot is 320 mm wide, so **nothing robot-sized hides in one
at stopping range**.

**Two numbers that constrain the whole design:**

- **The window height.** The measured 11.6 mm board fits h20's height with
  about 7.7 mm to spare. Each board is positioned so its optical centre, not
  its PCB centre, sits at mid-height. h25 was only ever chosen because the
  listing claimed the board was 15 mm tall.
- **The 27° cone hits the floor at ~490 mm** when aimed horizontally from
  this height, so a "4 m" sensor is really a 490 mm sensor here. The
  stopping distance is 400 mm. **That 90 mm margin is what caps safe
  speed**, and narrowing the SPAD ROI to 15° is the lever if it turns out
  too tight (floor moves to ~900 mm).

### Two pocket styles, compared on `sensor-coupon`

- **`front`** — the original. The pocket is open to the face and the board
  presses in from outside, held with a dab of glue. Pairs meet in a V at the
  face, with no flat rib between them.
- **`lip`** — the board drops in from the top face into a slot behind a lip
  at least 1.2 mm thick, looking out through a window. The window is the cap
  size, plus clearance, plus the 27° cone's spread across the thickest lip
  (about 8.9 mm). That leaves about 1.6 mm of lip on each side of the board
  to hold it. Inset pockets would close the rib to 0.8 mm, so `lip` pairs
  are spaced to keep a full 3 mm `RIB`.

Both styles put the wire slot flush with the pocket floor, where the pads
are, since the wires are soldered straight on. Checked on the full block:
both styles build manifold. The nearest any pocket comes to the disc
counterbore, a post notch, a hex pocket or the bore is 6.0 mm for `front` and
2.1 mm for `lip` (both against a post notch).

### What to check on `sensor-coupon`

1. **Does a board slide into each pocket?** Too tight → raise `SENSOR_FIT`.
2. **`lip`:** does the board drop in and stay put, and does the window leave
   the cap fully clear? The cap size is still taken from a photo.
3. **`front`:** does the V between the pair print cleanly, and does glue hold?
4. **Do the wire slots line up with the pads?**

`SUPERSEDED` records fastening schemes that were tried and rejected. No
geometry is kept — they failed on interface grounds, not dimensions — but the
reasons are what stop them being re-proposed later:

- **`long-screw`** — one long M4 per corner through the whole stack. DJI's
  M4-B has a **1 mm head**; catalogue low heads start near 2.2 mm, so that
  screw isn't a purchasable part.
- **`counterbore-insert`** — riser bolts down with counterbored screws, gimbal
  bolts up into heat-set inserts. Both sets share the 58 × 77 centres, so the
  insert pocket and the lower screw's driver access want the same space.
