#!/bin/bash
# Record the wizard, start to finish, as an animated GIF for the README.
#
#   scripts/record.sh docs/img/tui-wizard.gif           # English
#   scripts/record.sh docs/img/tui-wizard-ko.gif ko     # Korean
#
# It drives `malmok apply --tui --demo` in a detached tmux session and
# snapshots the pane five times a second. A recording made any other way is a
# recording of a mock-up; this one is the program, and it is a script so the
# next person to change the wizard can take it again.
#
# --demo means no node is contacted, which is why the install finishes in
# seconds. That is a simulation of the run, not a claim about how long a real
# one takes, and the README says so beside the image.
#
# Rendering goes through agg for the same reason scripts/screenshot.sh does:
# it draws into a real terminal grid, so double-width characters get two cells
# and the Korean screens keep their columns.
set -euo pipefail

out=${1:?usage: record.sh <out.gif> [lang]}
lang=${2:-en}

root=$(cd "$(dirname "$0")/.." && pwd)
bin=$root/bin/malmok
[ -x "$bin" ] || { echo "no bin/malmok -- run scripts/build.sh first" >&2; exit 1; }

work=$(mktemp -d)
trap 'tmux kill-session -t malmok-rec 2>/dev/null || true; rm -rf "$work"' EXIT
frames=$work/frames
mkdir -p "$frames"

# 120x34 is what the README images are sized for. -x/-y on new-session is not
# enough on its own: with no client attached tmux keeps its own default until
# the window is resized explicitly.
tmux kill-session -t malmok-rec 2>/dev/null || true
tmux new-session -d -s malmok-rec -x 120 -y 34
tmux resize-window -t malmok-rec -x 120 -y 34
sleep 1

key() { tmux send-keys -t malmok-rec "$@"; sleep "${DWELL:-1.4}"; }
type_slowly() {
  local s=$1 i
  for ((i = 0; i < ${#s}; i++)); do
    tmux send-keys -t malmok-rec -l "${s:i:1}"
    sleep 0.12
  done
  sleep 0.6
}

tmux send-keys -t malmok-rec "clear && '$bin' apply --tui --demo --lang $lang" Enter

# Snapshot until the drive below finishes. capture-pane fails once the session
# is gone, which is how the loop ends if anything goes wrong.
(
  for i in $(seq -w 1 400); do
    tmux capture-pane -t malmok-rec -e -p > "$frames/f$i.ans" 2>/dev/null || exit 0
    sleep 0.2
  done
) &
snap=$!

sleep 4                                   # the menu, long enough to read
key Enter                                 # Install a cluster
key Down; key Space                       # Where: other machines
key Enter
DWELL=0.4 key Down; DWELL=0.4 key Down; DWELL=0.4 key Down
DWELL=0.4 key Down; DWELL=0.4 key Down    # down to Server IP
tmux send-keys -t malmok-rec Enter; sleep 0.5
type_slowly "192.0.2.10"
key Escape
key Tab                                   # leave the fields for the buttons
key Enter                                 # Network
key Enter                                 # Options
key Enter                                 # Registry
key Enter                                 # Certificates
key Enter                                 # Gateway
DWELL=5 key Enter                         # Checks -- preflight runs
DWELL=2.5 key Enter                       # Summary -- worth a pause
tmux send-keys -t malmok-rec Enter        # Install

# The run ends on its own. Wait for the last screen rather than a fixed sleep.
for _ in $(seq 1 90); do
  tmux capture-pane -t malmok-rec -p 2>/dev/null | head -1 | grep -qE 'Finished|완료' && break
  sleep 1
done
sleep 3                                   # hold on the result

kill "$snap" 2>/dev/null || true
wait "$snap" 2>/dev/null || true
tmux kill-session -t malmok-rec 2>/dev/null || true

cp "$(dirname "$0")/mkcast_multi.py" "$work/mkcast_multi.py"

agg_url=https://github.com/asciinema/agg/releases/download/v1.9.0/agg-x86_64-unknown-linux-musl
curl -sfL -o "$work/agg" "$agg_url"
chmod +x "$work/agg"

docker run --rm --user root -v "$work:/w" alpine sh -c '
set -e
apk add --no-cache python3 ttf-dejavu font-noto-cjk fontconfig >/dev/null
fc-cache -f >/dev/null
python3 /w/mkcast_multi.py /w/frames /w/rec.cast 120 34 0.2 >/dev/null
/w/agg --font-family "Noto Sans Mono CJK KR" --font-size 20 --line-height 1.4 \
  --theme asciinema --fps-cap 10 --idle-time-limit 1 --last-frame-duration 3 \
  /w/rec.cast /w/out.gif 2>/dev/null
' >/dev/null

install -Dm644 "$work/out.gif" "$root/$out"
echo "wrote $out ($(du -h "$root/$out" | cut -f1))"
