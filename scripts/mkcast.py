"""Wrap a tmux ANSI capture as a one-frame asciicast for agg.

agg renders through a real terminal grid, which is the whole point: it gives
double-width characters two cells, and Hangul screens keep their columns. The
cursor is hidden first -- a terminal draws one, and a still of a finished
screen should not show it blinking at the end of a hint line.
"""
import json
import sys

ESC = "\x1b"

src, dst, cols, rows = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
screen = open(src, encoding="utf-8").read().rstrip("\n")
payload = ESC + "[?25l" + ESC + "[2J" + ESC + "[H" + screen.replace("\n", "\r\n")

with open(dst, "w", encoding="utf-8") as f:
    f.write(json.dumps({"version": 2, "width": cols, "height": rows}) + "\n")
    f.write(json.dumps([0.0, "o", payload]) + "\n")
    f.write(json.dumps([2.0, "o", ""]) + "\n")
print("wrote", dst)
