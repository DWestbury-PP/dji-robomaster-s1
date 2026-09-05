"""Fast tier: object detection for the console.

Runs as its own process, natively — deliberately not inside `s1teleop`, which is
pinned to amd64 under Rosetta by the DJI bridge and which owns a 20 Hz
safety-critical control loop. Inference belongs nowhere near either.

It pulls frames over HTTP and posts detections back, so it shares nothing with
the driver but a socket. That the console's transport is language-agnostic
(DECISIONS.md #14) is what makes a Python tier possible at all in a Go repo.

Detections are drawn on the console and written to the drive log. They actuate
nothing: what blocks automated movement triggers is the absence of depth, not
detection quality (DECISIONS.md #15).
"""

from __future__ import annotations

import argparse
import io
import sys
import time

import requests
from PIL import Image
from ultralytics import YOLO


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--console", default="http://localhost:8700",
                   help="Running s1teleop to pull frames from and post detections to.")
    p.add_argument("--model", default="yolo11n.pt",
                   help="Ultralytics weights. yolo11n is fastest; yolo11s is more accurate.")
    p.add_argument("--device", default="mps",
                   help="mps on Apple Silicon, cpu as a fallback.")
    p.add_argument("--rate", type=float, default=8.0,
                   help="Target detections per second. The console encodes at 15 fps, "
                        "so anything at or below that keeps up without queueing.")
    p.add_argument("--conf", type=float, default=0.35,
                   help="Confidence floor. Low values fill the view with boxes nobody trusts.")
    p.add_argument("--tier", default="fast", help="Tier name reported to the console.")
    p.add_argument("-v", "--verbose", action="store_true")
    return p.parse_args()


def pull_frame(session: requests.Session, console: str):
    """Fetch the newest frame and the identity needed to date a detection.

    Pulling rather than reading the pushed stream means a slow cycle can never
    accumulate stale frames in a socket — we always reason about now.
    """
    r = session.get(f"{console}/frame.jpg", timeout=5)
    if r.status_code != 200:
        return None
    h = r.headers
    return {
        "data": r.content,
        "seq": int(h.get("X-Frame-Seq", 0)),
        "captured_ms": int(h.get("X-Frame-Captured-Unix-Ms", 0)),
        "width": int(h.get("X-Frame-Width", 0)),
        "height": int(h.get("X-Frame-Height", 0)),
    }


def to_detections(result, conf_floor: float) -> list[dict]:
    out = []
    boxes = getattr(result, "boxes", None)
    if boxes is None:
        return out
    names = result.names
    for box in boxes:
        conf = float(box.conf[0])
        if conf < conf_floor:
            continue
        x1, y1, x2, y2 = (float(v) for v in box.xyxy[0])
        out.append({
            "label": names[int(box.cls[0])],
            "confidence": round(conf, 3),
            "box": [round(x1, 1), round(y1, 1), round(x2, 1), round(y2, 1)],
        })
    return out


def main() -> int:
    args = parse_args()

    print(f"loading {args.model} on {args.device}…", flush=True)
    model = YOLO(args.model)

    session = requests.Session()
    period = 1.0 / args.rate if args.rate > 0 else 0.0

    print(f"detecting from {args.console} at {args.rate:g}/s, conf ≥ {args.conf}\n", flush=True)

    misses = 0
    while True:
        cycle = time.monotonic()
        try:
            frame = pull_frame(session, args.console)
            if frame is None:
                misses += 1
                if misses % 20 == 1:
                    print("  no frame — is the console connected to a robot?", flush=True)
                time.sleep(1.0)
                continue
            misses = 0

            # Ultralytics wants a decoded image, not a byte stream. PIL rather
            # than a numpy array on purpose: numpy input is interpreted as BGR,
            # and silently swapped channels would degrade detection in a way
            # that looks like a bad model rather than a bug.
            image = Image.open(io.BytesIO(frame["data"])).convert("RGB")

            started = time.monotonic()
            results = model.predict(
                source=image, device=args.device, conf=args.conf, verbose=False,
            )
            latency_ms = (time.monotonic() - started) * 1000.0
            dets = to_detections(results[0], args.conf) if results else []

            session.post(f"{args.console}/perception", timeout=5, json={
                "tier": args.tier,
                "model": args.model,
                "frameSeq": frame["seq"],
                # RFC3339 with milliseconds, matching what the console expects.
                "capturedAt": time.strftime(
                    "%Y-%m-%dT%H:%M:%S", time.gmtime(frame["captured_ms"] / 1000)
                ) + f".{frame['captured_ms'] % 1000:03d}Z",
                "latencyMs": round(latency_ms, 1),
                "width": frame["width"], "height": frame["height"],
                "detections": dets,
            })

            if args.verbose:
                labels = ", ".join(sorted({d["label"] for d in dets})) or "nothing"
                print(f"  {latency_ms:6.1f} ms  {len(dets):2d}  {labels}", flush=True)

        except requests.RequestException as e:
            print(f"  console unreachable: {e}", flush=True)
            time.sleep(1.0)
        except KeyboardInterrupt:
            print("\nstopped")
            return 0
        except Exception as e:  # a detector must not die on one bad frame
            print(f"  frame failed: {e}", flush=True)
            time.sleep(0.5)

        elapsed = time.monotonic() - cycle
        if period > elapsed:
            time.sleep(period - elapsed)


if __name__ == "__main__":
    sys.exit(main())
