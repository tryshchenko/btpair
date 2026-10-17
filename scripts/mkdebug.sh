#!/bin/sh
# Builds fake drives under dev/Volumes for exercising bt without real disks:
#   WORK-MAIN / WORK-REPLICA   pair "work"; replica is an older, drifted copy
#   MEDIA-MAIN                 pair "media"; its replica is "unplugged" (dev/offline)
#   BLANK-A / BLANK-B         unpaired, for trying the new-pair wizard
set -eu
cd "$(dirname "$0")/.."
ROOT=dev/Volumes
BT=./bin/bt
RSYNC=$(command -v /opt/homebrew/bin/rsync || command -v rsync)

rm -rf dev/Volumes dev/offline
mkdir -p "$ROOT" dev/offline

rand() { head -c "$2" /dev/urandom > "$1"; }   # rand FILE BYTES
kb()   { echo $(( ($(od -An -N2 -tu2 /dev/urandom) % $1 + 1) * 1024 )); }

M=$ROOT/WORK-MAIN
echo "· generating $M"
for proj in atlas borealis cinder dune; do
  for sub in src docs assets; do
    mkdir -p "$M/Projects/$proj/$sub"
    for i in $(seq 1 25); do rand "$M/Projects/$proj/$sub/file_$i.dat" "$(kb 64)"; done
  done
done
for y in 2023 2024 2025; do
  mkdir -p "$M/Photos/$y"
  for i in $(seq 1 12); do rand "$M/Photos/$y/IMG_$y$(printf %04d "$i").HEIC" $(( $(kb 4096) + 1048576 )); done
done
mkdir -p "$M/Video" "$M/Documents/Taxes" "$M/Documents/Contracts"
rand "$M/Video/holiday-2024.mov" 120000000
rand "$M/Video/drone-flight.mp4" 80000000
rand "$M/Video/interview-raw.mov" 60000000
for i in $(seq 1 30); do rand "$M/Documents/Taxes/receipt_$i.pdf" "$(kb 300)"; done
for i in $(seq 1 10); do rand "$M/Documents/Contracts/contract_$i.pdf" "$(kb 900)"; done
echo "notes" > "$M/README.txt"

S=$ROOT/WORK-REPLICA
echo "· generating $S (older copy with drift)"
mkdir -p "$S"
"$RSYNC" -a "$M/" "$S/"
# things main gained since the last sync
rm -rf "$S/Projects/dune" "$S/Video/interview-raw.mov" "$S/Photos/2025"
# files edited on main since the last sync
for f in "$M"/Projects/atlas/src/file_1*.dat; do rand "$f" "$(kb 64)"; done
rand "$M/Video/drone-flight.mp4" 82000000
# stale files only the replica has — they will be moved to the replica's trash
mkdir -p "$S/Old Stuff/drafts"
for i in $(seq 1 15); do rand "$S/Old Stuff/drafts/draft_$i.txt" "$(kb 20)"; done
rand "$S/Documents/Taxes/receipt_old.pdf" 200000

MM=$ROOT/MEDIA-MAIN
echo "· generating $MM"
mkdir -p "$MM/Music" "$MM/Films"
for i in $(seq 1 40); do rand "$MM/Music/track_$i.flac" "$(kb 8192)"; done
rand "$MM/Films/film.mkv" 150000000
mkdir -p "$ROOT/MEDIA-REPLICA"

mkdir -p "$ROOT/BLANK-A" "$ROOT/BLANK-B"
echo "spare" > "$ROOT/BLANK-A/.keep"

echo "· pairing drives"
$BT init --dev "$ROOT" --main WORK-MAIN --replica WORK-REPLICA --pair work --label "Work SSDs"
$BT init --dev "$ROOT" --main MEDIA-MAIN --replica MEDIA-REPLICA --pair media --label "Music & films"
mv "$ROOT/MEDIA-REPLICA" dev/offline/   # simulate the replica being unplugged

du -sh "$ROOT"/* | sed 's/^/  /'
echo "done. run: make dev"
