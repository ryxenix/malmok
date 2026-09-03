#!/bin/bash
# Render a captured TUI screen to a PNG for the README.
#
# The input is an ANSI capture of a real screen, taken at 120x34:
#
#   tmux new-session -d -s shot -x 120 -y 34
#   tmux resize-window -t shot -x 120 -y 34        # -x/-y alone is not enough
#   tmux send-keys -t shot 'bin/malmok apply --tui --demo' Enter
#   ...drive the wizard...
#   tmux capture-pane -t shot -e -p > finished.ans
#
#   scripts/screenshot.sh finished.ans docs/img/tui-finished.png
#
# Not a mock-up. A screenshot that drifts from the program is worse than no
# screenshot, and the only way to keep it honest is to take it from the
# program -- which is also why this is a script rather than a note in a wiki.
#
# It goes through agg, the asciinema renderer, because agg draws into a real
# terminal grid. The obvious tool, freeze, lays glyphs out by counting
# characters: every Hangul syllable on the Korean screens occupies two cells
# and gets one, and the entire layout shears. agg gives them two.
#
# Noto Sans Mono CJK KR covers Latin, Hangul and the box-drawing the TUI is
# built from, at a consistent width. It has no U+21B5 -- the return arrow in
# every footer -- so DejaVu Sans Mono is installed beside it to be fallen back
# to for that one glyph.
set -euo pipefail

src=${1:?usage: screenshot.sh <capture.ans> <out.png> [cols] [rows]}
out=${2:?usage: screenshot.sh <capture.ans> <out.png> [cols] [rows]}
cols=${3:-120}
rows=${4:-34}
[ -f "$src" ] || { echo "no capture at $src" >&2; exit 1; }

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cp "$src" "$work/in.ans"
cp "$(dirname "$0")/mkcast.py" "$work/mkcast.py"

# agg ships prebuilt; building it from source would pull a Rust toolchain in
# for one image.
agg_url=https://github.com/asciinema/agg/releases/download/v1.9.0/agg-x86_64-unknown-linux-musl
curl -sfL -o "$work/agg" "$agg_url"
chmod +x "$work/agg"

docker run --rm --user root -v "$work:/w" alpine sh -c "
set -e
apk add --no-cache python3 ttf-dejavu font-noto-cjk fontconfig imagemagick >/dev/null
fc-cache -f >/dev/null
python3 /w/mkcast.py /w/in.ans /w/frame.cast $cols $rows >/dev/null
/w/agg --font-family 'Noto Sans Mono CJK KR' --font-size 28 --line-height 1.4 \
  --theme asciinema --last-frame-duration 1 /w/frame.cast /w/out.gif 2>/dev/null
magick /w/out.gif[0] /w/out.png
" >/dev/null

install -D "$work/out.png" "$root/$out"
echo "wrote $out ($(du -h "$root/$out" | cut -f1))"
