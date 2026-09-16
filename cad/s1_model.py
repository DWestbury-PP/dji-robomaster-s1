"""Rebuild the calibrated S1 reference model from the source FBX.

Idempotent: wipes any existing S1 collection and re-imports, so running it
twice gives the same scene rather than two robots.

WHY THIS IS A SCRIPT
The valuable part of the .blend is not the mesh - it is the calibration: a
scale factor derived from a caliper measurement, an alignment angle, and a
floor datum. Keeping that in a script means the reference model is
reproducible from a gitignored FBX plus tracked code, so no .blend ever has to
be versioned and no design stage depends on a binary someone has to keep.

WHAT THE MODEL IS AND IS NOT GOOD FOR
Calibrated on track width. Ground clearance then validates at TWO independent
points the calibration never saw: 29.9 mm against a measured 30.0 across the
main underside, and 27.8 mm against a measured 27.0 at a rear protrusion whose
position matches the battery latch. So the Y and Z axes are trustworthy to ~1%.

Measuring that clearance needs care. A box around the centre will clip the
edge of a wheel well and report the wheel instead - which is exactly what an
earlier hand-tuned box did, giving a right answer for the wrong reason. Here
we exclude whole objects that touch the floor, since the chassis underside by
definition cannot be one of them.

The X axis is NOT. It comes out 297.7 mm against a measured 315, about 5.5%
short - either an uneven stretch or missing bumpers; a static mesh cannot say
which. Do not take fore-aft dimensions from this model.

It also has no fasteners. The circles visible around the gimbal mount are
texture and mesh noise, not geometry: 51.5% of this mesh's edges are open, so
"circular boundary loop" is what unwelded shells look like, not a bolt circle.
Every fastener dimension in cad/ came from calipers or DJI's manual.
"""
import bpy, math, os
from mathutils import Vector, Matrix

FBX = os.path.expanduser("~/Documents/Source Code/dji-robomaster-s1/"
                         "3D-models/robomaster-s1-finchtest/source/xw0607.fbx")

TRACK_WIDTH_MM = 235.0   # calipers, outer wheel to outer wheel
BACKDROP       = "node_id259"   # zero-thickness billboard shipped in the FBX

def _wipe():
    for name in ("S1",):
        if name in bpy.data.collections:
            c = bpy.data.collections[name]
            for o in list(c.objects):
                bpy.data.objects.remove(o, do_unlink=True)
            bpy.data.collections.remove(c)
    for o in list(bpy.data.objects):
        if o.name.startswith(("S1_root", "node_id", "root")):
            bpy.data.objects.remove(o, do_unlink=True)

def _verts(objs):
    out = []
    for o in objs:
        mw = o.matrix_world
        for v in o.data.vertices:
            out.append(mw @ v.co)
    return out

def build():
    _wipe()
    coll = bpy.data.collections.new("S1")
    bpy.context.scene.collection.children.link(coll)

    before = {o.name for o in bpy.data.objects}
    bpy.ops.import_scene.fbx(filepath=FBX)
    new = [o for o in bpy.data.objects if o.name not in before]
    for o in new:
        for c in list(o.users_collection):
            c.objects.unlink(o)
        coll.objects.link(o)

    if BACKDROP in bpy.data.objects:
        bpy.data.objects.remove(bpy.data.objects[BACKDROP], do_unlink=True)

    root = bpy.data.objects.new("S1_root", None)
    coll.objects.link(root)
    for o in [x for x in coll.objects if x is not root and x.parent is None]:
        o.parent = root
        o.matrix_parent_inverse = Matrix.Identity(4)

    body = [o for o in coll.objects
            if o.type == "MESH" and min(o.dimensions) > 1e-6]

    # Align: rotate about Z to the angle that minimises the XY footprint.
    P = _verts(body)
    best = None
    for q in range(360):
        a = math.radians(q / 4.0)
        c, s = math.cos(a), math.sin(a)
        xs = [p.x*c - p.y*s for p in P]
        ys = [p.x*s + p.y*c for p in P]
        area = (max(xs)-min(xs)) * (max(ys)-min(ys))
        if best is None or area < best[0]:
            best = (area, a)
    root.matrix_world = Matrix.Rotation(best[1], 4, 'Z') @ root.matrix_world
    bpy.context.view_layer.update()

    # Scale on the measured track width, then sit it on the floor at the origin.
    P = _verts(body)
    width = max(p.y for p in P) - min(p.y for p in P)
    root.scale = tuple(s * (TRACK_WIDTH_MM/1000.0 / width) for s in root.scale)
    bpy.context.view_layer.update()

    P = _verts(body)
    root.location -= Vector((
        (max(p.x for p in P) + min(p.x for p in P)) / 2,
        (max(p.y for p in P) + min(p.y for p in P)) / 2,
        min(p.z for p in P)))
    bpy.context.view_layer.update()

    u = bpy.context.scene.unit_settings
    u.system, u.length_unit = 'METRIC', 'MILLIMETERS'

    P = _verts(body)
    L = (max(p.x for p in P)-min(p.x for p in P))*1000
    W = (max(p.y for p in P)-min(p.y for p in P))*1000
    H = (max(p.z for p in P)-min(p.z for p in P))*1000
    # Anything reaching the floor is a wheel; the underside cannot be.
    def _low(o):
        mw = o.matrix_world
        return min((mw @ v.co).z for v in o.data.vertices)
    chassis = [o for o in body if _low(o) >= 0.010]
    CP = _verts(chassis)
    core = [p for p in CP if abs(p.x) < 0.060 and abs(p.y) < 0.055]
    wide = [p for p in CP if abs(p.x) < 0.080 and abs(p.y) < 0.070]
    clr = min(p.z for p in core)*1000 if core else float("nan")
    prot = min(p.z for p in wide)*1000 if wide else float("nan")

    print(f"S1 reference rebuilt: {len(body)} meshes, aligned {math.degrees(best[1]):.2f} deg")
    print(f"  width  {W:6.1f} mm   (calibration anchor, measured 235.0)")
    print(f"  clear  {clr:6.2f} mm   (INDEPENDENT check, measured 30.0)")
    print(f"  rear   {prot:6.2f} mm   (INDEPENDENT check, measured 27.0 at the latch)")
    print(f"  length {L:6.1f} mm   (measured 315.0 - model is ~5.5% short, do not trust X)")
    print(f"  height {H:6.1f} mm")
    return coll

build()
