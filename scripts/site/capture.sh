#!/bin/sh
# Drives the TUI against the debug drives in tmux and saves ANSI captures of
# each screen to $OUT (default: site/build/screens). Run via `make site`.
set -eu
cd "$(dirname "$0")/../.."
OUT=${OUT:-site/build/screens}
COLS=${COLS:-112}
ROWS=${ROWS:-34}
export XDG_STATE_HOME="$(mktemp -d)"   # fresh step selections

make -s build
./scripts/mkdebug.sh >/dev/null
rm -rf "$OUT" && mkdir -p "$OUT"

S=btpair-site
tmux kill-session -t $S 2>/dev/null || true
start() {
  tmux new-session -d -s $S -x "$COLS" -y "$ROWS" -e COLORTERM=truecolor \
    "./bin/btpair --dev dev/Volumes --config dev/config.toml"
  sleep 1.5
}
k()   { tmux send-keys -t $S "$@"; }
cap() { tmux capture-pane -t $S -e -p > "$OUT/$1.ansi"; echo "  captured $1"; }

start
# Pass 1: a clean backup of the "work" pair.
k Enter;  sleep 1.5; cap picker
k Enter;  sleep 7;   cap run
sleep 11;            cap done

# Pass 2: new footage on the main drive and a failing step, to show how
# problems are reported.
k Enter; sleep 1
head -c 90000000 /dev/urandom > dev/Volumes/WORK-MAIN/Video/new-edit.mov
k Enter; sleep 1.5
k Space; k Enter; sleep 14
k Tab;   sleep 0.5;  cap issues
k Enter; sleep 1.5;  cap dashboard
k h;     sleep 0.8;  cap history
k Escape; sleep 0.4   # separate keys: "Esc n" together reads as Alt+n
k n;      sleep 0.5
k Enter Enter; sleep 0.3
k -l archive; k Enter; sleep 0.2
k -l "Archive SSDs (cold storage)"; k Enter; sleep 0.4; cap newpair

tmux kill-session -t $S
./scripts/mkdebug.sh >/dev/null   # leave the debug drives fresh
