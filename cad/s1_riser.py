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
    BORE_D            ESTIMATE from a photo - not yet measured
    CHASSIS_THREAD    ESTIMATE - a stud that bottoms out holds nothing
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
CHASSIS_THREAD = 5.0
# --- hardware -----------------------------------------------------------------
STANDOFF_AF    = 7.0     # hex across flats
# --- print fit ----------------------------------------------------------------
# X1C with a 0.4 mm nozzle puts vertical holes ~0.1-0.2 mm undersize. The hex
# pocket is sized to CAPTURE the standoff so it cannot spin while the top screw
# is torqued - too much clearance here and you lose that, so tune it on coupons.
HEX_CLEAR      = 0.35
MARGIN         = 9.0

OUT = os.path.expanduser(
    "~/Documents/Source Code/dji-robomaster-s1/3D-models/print")

VARIANTS = [
    dict(name="coupon", riser_h=5.0,  collar_h=0.0),   # pattern check, ~10 min
    dict(name="h15",    riser_h=15.0, collar_h=4.0),
    dict(name="h20",    riser_h=20.0, collar_h=4.0),
    dict(name="h25",    riser_h=25.0, collar_h=4.0),
]

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

def _box(coll, x, y, z, loc, name):
    bpy.ops.mesh.primitive_cube_add(size=1, location=[v*S for v in loc])
    o = bpy.context.object; o.name = name; o.scale = (x*S, y*S, z*S)
    for c in list(o.users_collection): c.objects.unlink(o)
    coll.objects.link(o); return o

def _bool(target, tool, op):
    bpy.context.view_layer.objects.active = target
    m = target.modifiers.new(name="b", type='BOOLEAN')
    m.operation = op; m.object = tool; m.solver = 'EXACT'
    bpy.ops.object.modifier_apply(modifier=m.name)

def build(coll, riser_h, collar_h, name="riser"):
    """Build one plate centred on the origin, sitting on z=0."""
    px, py = BOLT_X + 2*MARGIN, BOLT_Y + 2*MARGIN
    plate = _box(coll, px, py, riser_h, (0, 0, riser_h/2), name)

    if collar_h > 0:
        collar = _cyl(coll, BORE_D + 6.0, collar_h, (0, 0, riser_h + collar_h/2), "c_tmp")
        _bool(plate, collar, 'UNION')
        bpy.data.objects.remove(collar, do_unlink=True)

    tall = riser_h + collar_h + 40
    tools = [_cyl(coll, BORE_D, tall, (0, 0, riser_h/2), "t_bore")]
    for sx in (1, -1):
        for sy in (1, -1):
            tools.append(_cyl(coll, (STANDOFF_AF + HEX_CLEAR) / math.cos(math.pi/6),
                              tall, (sx*BOLT_X/2, sy*BOLT_Y/2, riser_h/2),
                              f"t_hex_{sx}_{sy}", verts=6))
    for t in tools:
        _bool(plate, t, 'DIFFERENCE')
        bpy.data.objects.remove(t, do_unlink=True)
    return plate

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
    for v in VARIANTS:
        o = build(coll, v["riser_h"], v["collar_h"], name=f"riser_{v['name']}")
        path = os.path.join(OUT, f"s1-riser-{v['name']}.stl")
        n = export(o, path)
        wall = MARGIN - (STANDOFF_AF + HEX_CLEAR)/2
        print(f"  {v['name']:<7} h={v['riser_h']:5.1f}  collar={v['collar_h']:4.1f}  "
              f"wall={wall:4.2f}  tris={len(o.data.polygons):4d}  {n/1024:6.1f} KB")
        o.hide_set(True)
    print(f"\nwrote {len(VARIANTS)} STLs to {OUT}")

main()
