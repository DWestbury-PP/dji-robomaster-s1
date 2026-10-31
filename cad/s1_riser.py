"""Parametric gimbal riser for the RoboMaster S1, built in Blender.

Run it against a live Blender through the addon socket (see cad/README.md).
Every dimension below is a named constant; change one and re-run to get a new
printable variant. Nothing here is drawn by hand, so a measurement that turns
out wrong is a one-line fix rather than a remodel.

THE STACK
    chassis  ->  4x M4 male-female standoff (metal, RISER_H long)  ->  gimbal
                 printed plate captured between them, carrying no load

The standoff's male stud threads into the chassis where DJI's screw went; its
female end takes that same M4x8 back, 1 mm head and all. So no interface on the
robot is adapted to anything, and the part is removable with a screwdriver.

The plate is deliberately NOT in the clamp path. Printed plastic creeping under
preload is what would eventually show up as camera shake, and this project
measures video stability (30.1 fps, 3.3 ms sigma), so a slow regression there
would be both real and hard to attribute.

PROVENANCE OF THE NUMBERS
    BOLT_X / BOLT_Y   calipers on the robot
    ORIG_SCREW_L      calipers: 9 mm overall less a 1 mm head
    M4                DJI Quick Start Guide v1.4, step 59 ("4x M4-B")
    MOUNT_Z / DECK_Z  measured off the calibrated model, whose Z axis was
                      validated against ground clearance to 0.8%
    BORE_D            confirmed by fit 2026-10-31 (fit coupon on the chassis).
                      NOT big enough for the gimbal's ~50.3 mm base disc -
                      see GIMBAL_DISC_D; the top interface is still open.
    CHASSIS_THREAD    calipers 2026-10-31: the full 5.8 mm stud threads in
                      and seats snug. No longer a risk.
    STANDOFF_*        calipers 2026-10-31: 6.7-6.8 A/F, bodies 20.0 / 24.9
    GIMBAL_DISC_*     calipers 2026-10-31; POST_* read off a photo
    SENSOR_*          calipers 2026-10-31 on a TOF400C: 19.9 x 11.6, 4.0 thick
                      including the back-side SMD parts. Cap window still
                      read off a photo - see SENSOR_CAP_*.

SENSOR COVERAGE
    Two pockets per face, splayed +-SPLAY, give 8 beams at 45 deg spacing.
    Boards sitting flat on a face would all aim the SAME way - that is
    redundancy, not coverage - and chamfering the corners to make diagonal
    faces is impossible here because the corners are where the standoffs are.
    Angling the pockets is what buys the arc without touching the outline.
"""
import bpy, math, os
from mathutils import Vector

# --- measured -----------------------------------------------------------------
BOLT_X, BOLT_Y = 58.0, 77.0
ORIG_SCREW_L   = 8.0
# --- from the calibrated model ------------------------------------------------
MOUNT_Z        = 108.0
# --- ESTIMATES: replace as measurements land ----------------------------------
BORE_D         = 46.0
CHASSIS_THREAD = 5.8     # whole stud engages
# --- gimbal interface (top face) ---------------------------------------------
# The gimbal's base disc is its electrical interface: two downward-facing 4-pin
# sockets that the chassis plugs mate into. It hangs GIMBAL_DISC_H below the
# arms, so the top face needs a counterbore it can never bottom out in -
# otherwise the disc, not the standoffs, carries the clamp.
GIMBAL_DISC_D  = 50.3    # calipers
GIMBAL_DISC_H  = 7.8     # calipers, below the underside of the arms
DISC_CLEAR_D   = 0.7     # on diameter
DISC_CLEAR_Z   = 0.5     # below the disc, so it can never bottom out
# Four small square posts on the gimbal ring at 0/90/180/270 deg, apparently
# reaching as low as the disc. UNVERIFIED - read off a photo; to be replaced by
# a clay impression. Notches are cut generous until then.
POST_R         = 27.0    # radius of the post centres
POST_W         = 6.0     # tangential
POST_L         = 6.0     # radial
# --- hardware -----------------------------------------------------------------
STANDOFF_AF    = 6.75    # hex across flats, measured 6.7-6.8
# Body length by nominal size, measured. The plate is cut PLATE_GAP shorter so
# the gimbal clamps metal-to-metal and the plastic never enters the clamp path.
STANDOFF_BODY  = {20: 20.0, 25: 24.9}
PLATE_GAP      = 0.4
# --- sensor payload: TOF400C (VL53L1X), measured 2026-10-31 -----------------
# The 27 deg cone puts the floor at ~490 mm when aimed horizontally from this
# height, which is only 90 mm beyond the 400 mm stopping distance. That margin
# is what caps safe speed, so it is a design constraint and not a detail.
SENSOR_W       = 19.9    # board width
SENSOR_H       = 11.6    # board height, pad row included - must clear the window
SENSOR_T       = 4.0     # PCB + cap + back-side SMD parts; NO header (wires
                         # are soldered straight to the pads)
SENSOR_EYE_UP  = 1.5     # optical centre sits this far above the board centre
                         # (4.0 mm to the top edge, 7.0 to the pad edge)
# Cap size drives the "lip" style's window.
SENSOR_CAP_W   = 11.8    # UNVERIFIED - scaled off a photo, not calipers
SENSOR_CAP_H   = 6.8     # UNVERIFIED - same
SENSOR_FIT     = 0.30    # clearance per pocket; tune on sensor-coupon
# Two ways of holding a board, compared side by side on sensor-coupon:
#   "front"  pocket open to the face; board pressed in from outside and glued
#   "lip"    board drops in from the top face into a slot behind a front lip,
#            and looks out through a window sized for the cap plus the cone
POCKET_STYLE   = "front" # the full block's choice, until the coupon decides
LIP_T          = 1.2     # thinnest lip, at the board's outermost corner
CONE_HALF      = 13.5    # VL53L1X full FoV is 27 deg
SPLAY          = 22.5    # half the 45 deg spacing
RIB            = 3.0     # plastic between the pair
WIRE_W, WIRE_H = 5.0, 3.0
# --- print fit ----------------------------------------------------------------
# X1C with a 0.4 mm nozzle puts vertical holes ~0.1-0.2 mm undersize. The hex
# pocket is sized to CAPTURE the standoff so it cannot spin while the top screw
# is torqued - too much clearance here and you lose that, so tune it on coupons.
HEX_CLEAR      = 0.35
MARGIN         = 9.0

OUT = os.path.expanduser(
    "~/Documents/Source Code/dji-robomaster-s1/3D-models/print")

# --- design stages -----------------------------------------------------------
# Each stage is a complete parameter set, so "go back to the 15 mm one" is an
# edit to BUILD rather than an archived binary somebody has to keep. Geometry
# is cheap to regenerate; the decision behind it is not, so the note travels
# with the numbers.
STAGES = {
    "fit-coupon": dict(riser_h=5.0, note=(
        "Bolt pattern and bore only, ~10 min to print. Checks the two things "
        "most likely to be wrong - hole positions and bore clearance - before "
        "committing to a tall print. Also the part to tune HEX_CLEAR on.")),
    "h15": dict(riser_h=15.0, note=(
        "Record only. Cannot carry a wall-mounted sensor, and predates the "
        "gimbal-disc counterbore.")),
    "h20": dict(riser_h=STANDOFF_BODY[20] - PLATE_GAP, disc=True, note=(
        "The gimbal-fit test: plain block with the disc counterbore. h20 "
        "because measured cable slack reaches a 20 mm lift but not 25. Checks "
        "that the gimbal seats on the standoffs, the disc clears, and both "
        "chassis plugs still reach their sockets.")),
    "h25": dict(riser_h=STANDOFF_BODY[25] - PLATE_GAP, disc=True, note=(
        "Record only. The gimbal's cables do not have the slack for 25 mm "
        "without extension leads.")),
    "sensor-coupon": dict(riser_h=STANDOFF_BODY[20] - PLATE_GAP, coupon=True,
                          note=(
        "One face segment at h20 height with the two pocket styles side by "
        "side: 'lip' (top-loaded, behind a windowed lip) at the -X end and "
        "'front' (pressed in from the face, glued) at the +X end. Settles "
        "SENSOR_FIT, the retention method, and whether the rib survives.")),
    "h20-sensors": dict(riser_h=STANDOFF_BODY[20] - PLATE_GAP, disc=True,
                        sensors=True, note=(
        "The payload block: 8 pockets at 45 deg, two per face, in "
        "POCKET_STYLE. Do not print before h20 has proven the gimbal fit and "
        "sensor-coupon has picked a style.")),
}
BUILD = ["h20", "sensor-coupon", "h20-sensors"]

# Height history: h15 was too short for any sensor window; h25 was chosen while
# the board was believed to be 15 mm tall; h20 is where it landed once the board
# measured 11.6 mm and the gimbal's cable slack turned out to stop short of 25.

# Fastening schemes tried and rejected. No geometry is kept - they failed on
# interface grounds rather than dimensions - but the reasons are what stop them
# being re-proposed six weeks from now.
SUPERSEDED = {
    "long-screw": (
        "One long M4 per corner through the whole stack. Dead because DJI's "
        "M4-B has a 1 mm head (measured: 9 mm overall, 8 mm shank). Catalogue "
        "low heads start around 2.2 mm, so a long screw with that head is not "
        "a purchasable part."),
    "counterbore-insert": (
        "Riser bolts down with its own screws counterbored inside it; gimbal "
        "bolts up into heat-set inserts in its top face. Dead because both "
        "fastener sets sit on the same 58 x 77 centres - the insert pocket and "
        "the driver access for the lower screw want the same space."),
}

S = 0.001

def _fresh(name):
    if name in bpy.data.collections:
        c = bpy.data.collections[name]
        for o in list(c.objects):
            bpy.data.objects.remove(o, do_unlink=True)
        bpy.data.collections.remove(c)
    c = bpy.data.collections.new(name)
    bpy.context.scene.collection.children.link(c)
    return c

def _cyl(coll, d, h, loc, name, verts=64):
    bpy.ops.mesh.primitive_cylinder_add(vertices=verts, radius=d*S/2,
                                        depth=h*S, location=[v*S for v in loc])
    o = bpy.context.object; o.name = name
    for c in list(o.users_collection): c.objects.unlink(o)
    coll.objects.link(o); return o

def _box(coll, x, y, z, loc, name, rot_z=0.0):
    bpy.ops.mesh.primitive_cube_add(size=1, location=[v*S for v in loc])
    o = bpy.context.object; o.name = name; o.scale = (x*S, y*S, z*S)
    if rot_z:
        o.rotation_euler = (0.0, 0.0, math.radians(rot_z))
    for c in list(o.users_collection): c.objects.unlink(o)
    coll.objects.link(o); return o


def sensor_cutters(coll, riser_h, faces=(0.0, 90.0, 180.0, 270.0),
                   styles=None):
    """Two splayed pockets per face, plus a wire slot behind each.

    `faces` are outward normals in degrees CCW from +X. `styles` gives the
    pocket style for the (+, -) member of each pair; default POCKET_STYLE for
    both. The board is placed so its optical centre sits at mid-height, and
    the wire slot leaves flush with the pocket floor, where the pads are.
    """
    styles = styles or (POCKET_STYLE, POCKET_STYLE)
    px, py = BOLT_X + 2*MARGIN, BOLT_Y + 2*MARGIN
    sp    = math.radians(SPLAY)
    proj  = SENSOR_W * math.cos(sp)
    eye_z = riser_h / 2
    pz    = eye_z - SENSOR_EYE_UP              # pocket centre height
    ph    = SENSOR_H + SENSOR_FIT
    floor = pz - ph/2
    out   = []
    for a in faces:
        ar = math.radians(a)
        n   = (math.cos(ar), math.sin(ar))
        tan = (-math.sin(ar), math.cos(ar))
        half = px/2 if abs(n[0]) > 0.5 else py/2
        for sgn, style in zip((1, -1), styles):
            t = sgn * (proj/2 + RIB/2)
            aim = a + sgn*SPLAY          # this board's outward normal
            rot = aim - 90.0             # a box's local +Y starts at 90 deg
            ax, ay = math.cos(math.radians(aim)), math.sin(math.radians(aim))
            tag = f"{a:.0f}_{sgn}"
            if style == "front":
                # Box centred ON the face plane: one cut makes cavity and
                # window, and the board presses in from outside.
                depth = SENSOR_T + SENSOR_FIT
                cx, cy = n[0]*half + tan[0]*t, n[1]*half + tan[1]*t
                out.append(_box(coll, SENSOR_W + 2*SENSOR_FIT, 2*depth, ph,
                                (cx, cy, pz), f"t_pkt_{tag}", rot_z=rot))
                bx, by = cx - ax*depth, cy - ay*depth      # back of board
            elif style == "lip":
                # Board centre placed so its outermost front corner is LIP_T
                # inside the face; the pocket runs up through the top face.
                tt = SENSOR_T + SENSOR_FIT
                # Inset pockets bring their back corners together at the rib
                # (0.8 mm at the "front" spacing), so space them to leave RIB.
                t = sgn * ((SENSOR_W/2 + SENSOR_FIT)*math.cos(sp)
                           + tt/2*math.sin(sp) + RIB/2)
                inset = LIP_T + tt/2*math.cos(sp) + (SENSOR_W/2)*math.sin(sp)
                cx = n[0]*(half - inset) + tan[0]*t
                cy = n[1]*(half - inset) + tan[1]*t
                top = riser_h + 1.0
                out.append(_box(coll, SENSOR_W + 2*SENSOR_FIT, tt,
                                top - floor, (cx, cy, (top + floor)/2),
                                f"t_pkt_{tag}", rot_z=rot))
                # Window: cap plus clearance, widened for the cone over the
                # thickest lip it has to look through.
                lip_max = LIP_T + SENSOR_W*math.sin(sp)
                grow = 2*lip_max*math.tan(math.radians(CONE_HALF)) + 0.6
                wl = 20.0
                wx, wy = cx + ax*(tt/2 + wl/2), cy + ay*(tt/2 + wl/2)
                out.append(_box(coll, SENSOR_CAP_W + grow, wl,
                                SENSOR_CAP_H + grow, (wx, wy, eye_z),
                                f"t_win_{tag}", rot_z=rot))
                bx, by = cx - ax*tt/2, cy - ay*tt/2
            else:
                raise ValueError(f"unknown pocket style {style!r}")
            # wire slot: inboard from the back of the board, flush with floor
            out.append(_box(coll, WIRE_W, 30.0, WIRE_H,
                            (bx - ax*15.0, by - ay*15.0, floor + WIRE_H/2),
                            f"t_wire_{tag}", rot_z=rot))
    return out

def disc_cutters(coll, riser_h):
    """Counterbore for the gimbal's base disc, plus notches for its posts."""
    depth = GIMBAL_DISC_H - PLATE_GAP + DISC_CLEAR_Z
    zc = riser_h                     # tools centred on the top face
    out = [_cyl(coll, GIMBAL_DISC_D + DISC_CLEAR_D, 2*depth, (0, 0, zc),
                "t_disc", verts=128)]
    for a in (0.0, 90.0, 180.0, 270.0):
        ar = math.radians(a)
        out.append(_box(coll, POST_L, POST_W, 2*depth,
                        (POST_R*math.cos(ar), POST_R*math.sin(ar), zc),
                        f"t_post_{a:.0f}", rot_z=a))
    return out

def _bool(target, tool, op):
    bpy.context.view_layer.objects.active = target
    m = target.modifiers.new(name="b", type='BOOLEAN')
    m.operation = op; m.object = tool; m.solver = 'EXACT'
    bpy.ops.object.modifier_apply(modifier=m.name)

def build(coll, riser_h, name="riser", sensors=False, disc=False):
    """Build one plate centred on the origin, sitting on z=0."""
    px, py = BOLT_X + 2*MARGIN, BOLT_Y + 2*MARGIN
    plate = _box(coll, px, py, riser_h, (0, 0, riser_h/2), name)

    tall = riser_h + 40
    tools = [_cyl(coll, BORE_D, tall, (0, 0, riser_h/2), "t_bore")]
    for sx in (1, -1):
        for sy in (1, -1):
            tools.append(_cyl(coll, (STANDOFF_AF + HEX_CLEAR) / math.cos(math.pi/6),
                              tall, (sx*BOLT_X/2, sy*BOLT_Y/2, riser_h/2),
                              f"t_hex_{sx}_{sy}", verts=6))
    if disc:
        tools += disc_cutters(coll, riser_h)
    if sensors:
        tools += sensor_cutters(coll, riser_h)
    for t in tools:
        _bool(plate, t, 'DIFFERENCE')
        bpy.data.objects.remove(t, do_unlink=True)
    return plate


def build_sensor_coupon(coll, name="riser_sensor-coupon"):
    """One face segment carrying a splayed pair. ~15 min to print.

    Tests the three things the full block cannot tell us cheaply: does a
    board slide into the pocket, is SENSOR_FIT right, and does the 22.5 deg
    splay leave enough plastic in the centre rib.
    """
    px, py = BOLT_X + 2*MARGIN, BOLT_Y + 2*MARGIN
    h = STAGES["sensor-coupon"]["riser_h"]
    blk = _box(coll, px, 22.0, h, (0, py/2 - 11.0, h/2), name)
    for t in sensor_cutters(coll, h, faces=(90.0,), styles=("lip", "front")):
        _bool(blk, t, 'DIFFERENCE')
        bpy.data.objects.remove(t, do_unlink=True)
    return blk

def export(obj, path):
    """STL in millimetres. Blender works in metres; Bambu Studio assumes mm."""
    bpy.ops.object.select_all(action='DESELECT')
    obj.select_set(True)
    bpy.context.view_layer.objects.active = obj
    bpy.ops.wm.stl_export(filepath=path, export_selected_objects=True,
                          global_scale=1000.0, apply_modifiers=True)
    return os.path.getsize(path)

def main():
    os.makedirs(OUT, exist_ok=True)
    coll = _fresh("RISER_BUILD")
    print(f"bolt pattern {BOLT_X:.0f} x {BOLT_Y:.0f} mm, hex pocket "
          f"{STANDOFF_AF + HEX_CLEAR:.2f} mm A/F, bore {BORE_D:.0f} mm\n")
    for key in BUILD:
        v = STAGES[key]
        if v.get("coupon"):
            o = build_sensor_coupon(coll, name=f"riser_{key}")
        else:
            o = build(coll, v["riser_h"], name=f"riser_{key}",
                      sensors=v.get("sensors", False), disc=v.get("disc", False))
        path = os.path.join(OUT, f"s1-riser-{key}.stl")
        n = export(o, path)
        wall = MARGIN - (STANDOFF_AF + HEX_CLEAR)/2
        tag = " +sensors" if v.get("sensors") else ""
        print(f"  {key:<14} h={v['riser_h']:5.1f}  "
              f"wall={wall:4.2f}  tris={len(o.data.polygons):4d}  "
              f"{n/1024:6.1f} KB{tag}")
        o.hide_set(True)
    print(f"\nwrote {len(BUILD)} STLs to {OUT}")
    if SUPERSEDED:
        print("\nsuperseded schemes (reasons kept, geometry not):")
        for k in SUPERSEDED:
            print(f"  {k}")

main()
