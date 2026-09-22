#!/usr/bin/env python3
"""Probe TypeSafe's Jev against the decisions this project would ask of it.

Reproduces docs/BAKEOFF-JEV.md. Stdlib only, so it needs no environment:

    python3 scripts/jev-probe.py            # all parts
    python3 scripts/jev-probe.py geometry   # one part

Reads JEV_API_KEY from the environment or .env, and never prints it.

The question under test is not "is Jev good" but "where in the decision
hierarchy does it belong". Part 1 pits it against the arithmetic it would
have to beat to earn a place on the reflex tier; part 2 gives it the job
arithmetic cannot do; part 3 feeds it faults to see whether `confidence`
is load-bearing enough to gate on.
"""
import json
import os
import statistics
import sys
import time
import urllib.request

URL = "https://api.typesafe.ai/v1/systemone"
MODEL = "jev-latest"
STOP_MM = 400  # the S1 needs this much to stop at 0.5 m/s


def api_key():
    key = os.environ.get("JEV_API_KEY")
    if key:
        return key.strip()
    here = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    try:
        with open(os.path.join(here, ".env")) as f:
            for line in f:
                if line.startswith("JEV_API_KEY="):
                    return line.split("=", 1)[1].strip().strip('"').strip("'")
    except FileNotFoundError:
        pass
    sys.exit("JEV_API_KEY not set and not found in .env")


KEY = api_key()

# The robot, described the way the ESP32 would have to describe it. Part 1
# shows how much of the result rides on this paragraph.
CTX = (
    "Vehicle: DJI RoboMaster S1, 320 mm long, 235 mm wide, mecanum wheels, "
    "so it can strafe sideways without turning. Travelling forward at 0.5 m/s "
    f"and needs {STOP_MM} mm to stop. Sensors are VL53L1X time-of-flight, "
    "range 40-4000 mm; 4000 means nothing detected."
)

LAT = []


def ask(state, questions, trials=2):
    body = {"state": state, "model": MODEL, "questions": questions}
    out = None
    for _ in range(trials):
        req = urllib.request.Request(
            URL,
            data=json.dumps(body).encode(),
            headers={"Authorization": f"Bearer {KEY}",
                     "Content-Type": "application/json"},
        )
        t = time.perf_counter()
        with urllib.request.urlopen(req) as r:
            out = json.loads(r.read())
        LAT.append((time.perf_counter() - t) * 1000)
    return out


def snap(f, l, r, rear):
    return (f"Sensor snapshot: front {f} mm, left {l} mm, right {r} mm, "
            f"rear {rear} mm.")


def digest(f, l, r, rear):
    """What the ESP32 would send if it did the comparison itself."""
    names = {"front": f, "left": l, "right": r, "rear": rear}
    clear = [k for k, v in names.items() if v >= STOP_MM]
    return (f"Clearance check at {STOP_MM} mm. Directions with room: "
            f"{', '.join(clear) if clear else 'none'}. "
            f"Most open direction: {max(names, key=names.get)}.")


def arithmetic(f, l, r, rear):
    """The entire competing policy for the reflex tier."""
    if f >= STOP_MM:
        return "forward"
    if max(l, r) >= STOP_MM:
        return "strafe_left" if l > r else "strafe_right"
    if rear >= STOP_MM:
        return "reverse"
    return "stop"


# Two wordings of the same five moves. V1 was the first draft; its `stop`
# description matches any cluttered scene, and because Jev scores every
# option independently that alone cost it half the geometry run.
MOVES_V1 = {
    "forward": "Continue straight ahead; the way ahead is clear.",
    "strafe_left": "Slide sideways to the left without turning.",
    "strafe_right": "Slide sideways to the right without turning.",
    "reverse": "Back away; every forward and sideways route is blocked.",
    "stop": "Halt and take no action; the situation is unclear or unsafe.",
}
MOVES_V2 = {
    "forward": f"The front reading is the largest and exceeds {STOP_MM} mm.",
    "strafe_left": f"The left reading exceeds {STOP_MM} mm and the front does not.",
    "strafe_right": f"The right reading exceeds {STOP_MM} mm and the front does not.",
    "reverse": f"Only the rear reading exceeds {STOP_MM} mm.",
    "stop": f"No reading in any direction exceeds {STOP_MM} mm.",
}


def move_q(criteria):
    return {"move": {"type": "choice", "criteria": criteria,
                     "instructions": "Which single move should the vehicle "
                                     "make next?"}}


# (front, left, right, rear, expected)
GEOMETRY = [
    (220, 1800, 1750, 900, "strafe_left"),
    (4000, 4000, 4000, 4000, "forward"),
    (180, 210, 195, 2200, "reverse"),
    (3200, 260, 255, 1500, "forward"),
    (300, 1500, 1450, 900, "strafe_left"),
    (150, 140, 160, 170, "stop"),
    (410, 2000, 2000, 2000, "forward"),   # the boundary case
    (390, 100, 3000, 800, "strafe_right"),
]


def part_geometry():
    print("=" * 78)
    print("PART 1 - GEOMETRY: Jev vs. a five-line arithmetic policy")
    print("=" * 78)
    print(f"{'sensors f/l/r/rear':24s} {'expected':13s} {'V1 wording':13s} "
          f"{'V2 wording':13s} {'V3 digested':13s}")
    print("-" * 78)
    score = {"arith": 0, "v1": 0, "v2": 0, "v3": 0}
    for f, l, r, rear, exp in GEOMETRY:
        score["arith"] += arithmetic(f, l, r, rear) == exp
        s = CTX + " " + snap(f, l, r, rear)
        v1 = ask(s, move_q(MOVES_V1))["answers"]["move"]["choice"]
        v2 = ask(s, move_q(MOVES_V2))["answers"]["move"]["choice"]
        v3 = ask(CTX + " " + digest(f, l, r, rear),
                 move_q(MOVES_V2))["answers"]["move"]["choice"]
        for k, v in (("v1", v1), ("v2", v2), ("v3", v3)):
            score[k] += v == exp
        mark = lambda c: c if c == exp else c + "*"
        print(f"{f:5d}/{l:4d}/{r:4d}/{rear:4d}    {exp:13s} "
              f"{mark(v1):13s} {mark(v2):13s} {mark(v3):13s}")
    n = len(GEOMETRY)
    print("-" * 78)
    print(f"  arithmetic {score['arith']}/{n}   V1 {score['v1']}/{n}   "
          f"V2 {score['v2']}/{n}   V3 {score['v3']}/{n}      (* = wrong)")


# Real captions. The first is the canonical corpus failure: gemma4 wrote
# this sentence and returned clear_path:ahead in the same response
# (BAKEOFF.md:31).
CAPTIONS = [
    ("frame0208 gemma4 (real)",
     "The path directly ahead is partially obstructed by a dense, linear "
     "arrangement of stacked plastic bins.", "none"),
    ("frame0166 aquarium (real)",
     "An aquarium on a stand sits to the left at some distance. Curtains hang "
     "across a doorway ahead. The floor between is open and unobstructed.",
     "ahead"),
    ("clear hallway",
     "A long empty hallway extends straight ahead with bare walls on both "
     "sides and nothing on the floor.", "ahead"),
    ("wall dead ahead",
     "A painted wall fills the entire view about half a metre in front of the "
     "camera. There is no visible way past it.", "none"),
    ("clutter left, open right",
     "Boxes and a laundry basket are piled against the left wall. To the "
     "right the floor is clear and leads toward a lit doorway.", "right"),
    ("doorway ahead, narrow",
     "Directly ahead is an open doorway, narrow but passable, with clear "
     "floor leading up to it.", "ahead"),
    ("person standing",
     "A person in jeans is standing directly in front of the camera, close "
     "enough to fill most of the frame.", "none"),
]

CLEAR_PATH = {"clear_path": {"type": "choice", "instructions":
              "Based only on this description, which direction can the robot "
              "drive?", "criteria": {
                  "ahead": "The description says the way straight ahead is "
                           "open or unobstructed.",
                  "left": "The description says the left is open while ahead "
                          "is not.",
                  "right": "The description says the right is open while "
                           "ahead is not.",
                  "none": "The description says something obstructs or blocks "
                          "the way ahead."}}}


def part_captions():
    print()
    print("=" * 78)
    print("PART 2 - PROSE: Jev as the structuring layer over VLM captions")
    print("=" * 78)
    print(f"{'caption':30s} {'truth':7s} {'jev':7s} conf")
    print("-" * 78)
    ok = 0
    for name, cap, truth in CAPTIONS:
        a = ask(cap, CLEAR_PATH)["answers"]["clear_path"]
        ok += a["choice"] == truth
        flag = "" if a["choice"] == truth else "  <-- MISS"
        print(f"{name:30s} {truth:7s} {a['choice']:7s} "
              f"{a['confidence']:.2f}{flag}")
    print("-" * 78)
    print(f"  {ok}/{len(CAPTIONS)}   (gemma4 returned clear_path:ahead on "
          f"6 of 6 frames -- BAKEOFF.md:34)")


FAULTS = [
    ("all zeros (sensor dead)", snap(0, 0, 0, 0)),
    ("negative reading", snap(-1, 2000, 2000, 2000)),
    ("out of range high", snap(99999, 1000, 1000, 1000)),
    ("known-faulty front sensor", snap(3000, 2000, 2000, 2000) +
     " Note: the front sensor is known to be faulty and reads 3000 when covered."),
    ("garbage", "Sensor snapshot: ?????"),
]


def part_faults():
    print()
    print("=" * 78)
    print("PART 3 - FAULTS: is `confidence` safe to gate on? (safe = stop/reverse)")
    print("=" * 78)
    for name, st in FAULTS:
        a = ask(CTX + " " + st, move_q(MOVES_V1))["answers"]["move"]
        verdict = "safe" if a["choice"] in ("stop", "reverse") else "UNSAFE"
        print(f"{name:30s} {a['choice']:13s} conf={a['confidence']:.2f}  {verdict}")


PARTS = {"geometry": part_geometry, "captions": part_captions,
         "faults": part_faults}

if __name__ == "__main__":
    want = sys.argv[1:] or list(PARTS)
    for name in want:
        if name not in PARTS:
            sys.exit(f"unknown part {name!r}; choose from {', '.join(PARTS)}")
        PARTS[name]()
    LAT.sort()
    print()
    print(f"latency over {len(LAT)} calls: min {LAT[0]:.0f}  "
          f"median {statistics.median(LAT):.0f}  "
          f"p90 {LAT[int(len(LAT) * 0.9)]:.0f}  max {LAT[-1]:.0f} ms"
          f"   (the deadman is 250 ms)")
