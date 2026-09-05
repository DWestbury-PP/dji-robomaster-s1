# Fast tier — object detection

Runs as its own process, natively on arm64. Deliberately **not** inside
`s1teleop`, which is pinned to amd64 under Rosetta by the DJI bridge and owns a
20 Hz safety-critical control loop.

```bash
cd perception/detector
uv run detect.py -v                        # yolo11n on MPS at 8/s
uv run detect.py --model yolo11s.pt --rate 5 -v
uv run detect.py --device cpu              # fallback
```

Weights download on first run. Foveate measured yolo11s at **8.2 ms** on ONNX
Runtime/CoreML and **10.8 ms** on Ultralytics/MPS; yolo11n is faster still.

It pulls `/frame.jpg` rather than reading the pushed stream, so a slow cycle can
never accumulate stale frames in a socket — the detector always reasons about
now.

**Detections actuate nothing.** They are drawn on the console and written to the
drive log. What blocks automated movement triggers is the absence of depth, not
detection quality (DECISIONS.md #15) — a better detector would not change that;
a depth sensor or a calibrated ground plane would.
