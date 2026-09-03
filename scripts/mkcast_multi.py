"""Build a multi-frame asciicast from periodic tmux screen snapshots.

Each frame is a full repaint -- clear, home, the screen -- rather than a diff.
That is wasteful in the cast and free in the GIF: agg renders frames, and the
encoder collapses the runs of identical pixels either way. It also means a
dropped snapshot costs one stale frame instead of corrupting everything after
it, which a diff stream cannot say.
"""
import glob
import json
import os
import sys

ESC = "\x1b"

frames_dir, dst, cols, rows, interval = (
    sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), float(sys.argv[5])
)

paths = sorted(glob.glob(os.path.join(frames_dir, "*.ans")))
if not paths:
    sys.exit(f"no frames in {frames_dir}")

with open(dst, "w", encoding="utf-8") as f:
    f.write(json.dumps({"version": 2, "width": cols, "height": rows}) + "\n")
    f.write(json.dumps([0.0, "o", ESC + "[?25l"]) + "\n")
    previous = None
    for i, p in enumerate(paths):
        screen = open(p, encoding="utf-8").read().rstrip("\n")
        # An unchanged screen needs no frame; the one before it just lasts
        # longer. This is most of the saving on a wizard, where the operator
        # is reading and nothing moves.
        if screen == previous:
            continue
        previous = screen
        # Home, then every line followed by erase-to-end-of-line, then
        # erase-to-end-of-screen. Without the per-line erase a short line
        # leaves the tail of whatever was on that row in the frame before,
        # and the wizard's screens bleed into each other.
        body = (ESC + "[K\r\n").join(screen.split("\n"))
        payload = ESC + "[H" + body + ESC + "[K" + ESC + "[J"
        f.write(json.dumps([round(i * interval, 3), "o", payload]) + "\n")

print(f"wrote {dst}: {len(paths)} snapshots, {sum(1 for _ in open(dst)) - 2} frames")
