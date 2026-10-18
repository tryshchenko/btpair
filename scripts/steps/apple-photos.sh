#!/bin/bash
# Copies the Apple Photos library onto the main drive, after checking that
# every original is actually on this Mac. Meant to run as a btpair shell step:
#
#   [steps.apple-photos]
#   type = "shell"
#   title = "Apple Photos"
#   default = true
#   skip_if = '"/path/to/apple-photos.sh" plan'
#   run = '"/path/to/apple-photos.sh"'
#
#   [steps.apple-photos-force]
#   type = "shell"
#   title = "Apple Photos (force, emergency)"
#   default = false
#   run = '"/path/to/apple-photos.sh" --force'
#
# Why the check: with "Optimize Mac Storage", or while iCloud is still
# downloading, some originals exist only in iCloud. Copying then would
# leave holes in the backup and --delete would remove the originals the
# previous copy still had. So a normal run refuses unless every original
# older than PHOTOS_GRACE_HOURS is present on disk. The database's own
# "local" flag is not trusted: it can claim a photo is local when only its
# thumbnails are, so each original file is checked directly.
#
# --force (or PHOTOS_FORCE=1) copies anyway, without --delete, so nothing
# already in the backup is removed. For when the Mac is dying, not for
# routine use.
#
# Settings (environment, e.g. via the step's `env` table):
#   PHOTOS_LIBRARY      default ~/Pictures/Photos Library.photoslibrary
#   PHOTOS_DEST         folder on the main drive, default Photos/Apple
#   PHOTOS_GRACE_HOURS  recent imports allowed to be missing, default 24
#   RSYNC               default /opt/homebrew/bin/rsync, else rsync
#
# Restore: copy the .photoslibrary back to an APFS disk and open it with
# Photos (hold Option while launching). Photos won't open it from exFAT.

set -uo pipefail

LIB="${PHOTOS_LIBRARY:-$HOME/Pictures/Photos Library.photoslibrary}"
DEST_REL="${PHOTOS_DEST:-Photos/Apple}"
GRACE_HOURS="${PHOTOS_GRACE_HOURS:-24}"
FORCE="${PHOTOS_FORCE:-0}"
MODE=run
for a in "$@"; do
	case "$a" in
	plan) MODE=plan ;;
	--force) FORCE=1 ;;
	*) echo "unknown argument: $a" >&2; exit 2 ;;
	esac
done
[ "$FORCE" = 1 ] || FORCE=0

RSYNC="${RSYNC:-}"
if [ -z "$RSYNC" ]; then
	RSYNC=/opt/homebrew/bin/rsync
	[ -x "$RSYNC" ] || RSYNC=rsync
fi

DB="$LIB/database/Photos.sqlite"
DEST="${BT_MAIN:-.}/$DEST_REL/$(basename "$LIB")"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# fail prints the reason as the step's error. In plan mode it becomes the
# picker summary instead (plan always exits 1 so the step is never skipped).
fail() {
	echo "$1" >&2
	exit 1
}

warn() { echo "::warn $1"; }

sql() { sqlite3 -readonly -separator '|' "$DB" "$1"; }

# --- library sanity -------------------------------------------------------

[ -d "$LIB" ] || fail "no Photos library at $LIB (set PHOTOS_LIBRARY)"
command -v sqlite3 >/dev/null || fail "sqlite3 not found"
if ! sql "SELECT 1;" >/dev/null 2>"$TMP/err"; then
	if grep -qiE 'authoriz|not permitted|unable to open' "$TMP/err"; then
		fail "cannot read the Photos database — give your terminal Full Disk Access (System Settings ▸ Privacy & Security)"
	fi
	fail "cannot read the Photos database: $(head -1 "$TMP/err")"
fi

cols=$(sql "SELECT name FROM pragma_table_info('ZASSET');")
for c in ZDIRECTORY ZFILENAME ZTRASHEDSTATE ZADDEDDATE ZSAVEDASSETTYPE ZCLOUDBATCHPUBLISHDATE; do
	grep -qx "$c" <<<"$cols" ||
		fail "unfamiliar Photos database layout (ZASSET.$c missing) — this macOS version needs a script update; use the force step meanwhile"
done

photos_running=0
pgrep -xq Photos && photos_running=1

# --- completeness check ---------------------------------------------------

# Every asset in the library except trashed ones and Shared Album items
# (those are reduced-size copies kept outside originals/). Dates are Core
# Data timestamps: seconds since 2001-01-01.
sql "SELECT a.ZDIRECTORY || '/' || a.ZFILENAME,
            CAST(a.ZADDEDDATE + 978307200 AS INTEGER),
            COALESCE(strftime('%Y-%m-%d', a.ZDATECREATED + 978307200, 'unixepoch'), '?'),
            COALESCE(aa.ZORIGINALFILENAME, a.ZFILENAME)
     FROM ZASSET a
     LEFT JOIN ZADDITIONALASSETATTRIBUTES aa ON aa.ZASSET = a.Z_PK
     WHERE a.ZTRASHEDSTATE = 0
       AND a.ZSAVEDASSETTYPE <> 4
       AND a.ZCLOUDBATCHPUBLISHDATE IS NULL;" >"$TMP/assets" 2>"$TMP/err" ||
	fail "Photos database query failed: $(head -1 "$TMP/err")"

(cd "$LIB/originals" 2>/dev/null && find . -type f | sed 's|^\./||') >"$TMP/files"

cutoff=$(($(date +%s) - GRACE_HOURS * 3600))
awk -F'|' -v cutoff="$cutoff" -v out="$TMP/missing" '
	NR == FNR { have[$0] = 1; next }
	{ total++ }
	!($1 in have) { if ($2 >= cutoff) recent++; else { old++; print $3 "  " $4 > out } }
	END { printf "%d %d %d\n", total, old + 0, recent + 0 }
' "$TMP/files" "$TMP/assets" >"$TMP/counts"
read -r total missing recent <"$TMP/counts"

fmt() { printf "%'d" "$1"; }
human() {
	awk -v b="$1" 'BEGIN { split("B KB MB GB TB", u); i = 1
		while (b >= 1000 && i < 5) { b /= 1000; i++ }
		printf (i == 1 ? "%d %s" : "%.1f %s"), b, u[i] }'
}

if [ "$MODE" = plan ]; then
	if [ "$total" -eq 0 ]; then
		echo "library is empty"
	elif [ "$missing" -gt 0 ]; then
		echo "⚠ $(fmt "$missing") of $(fmt "$total") originals are not on this Mac"
	elif [ "$photos_running" = 1 ]; then
		echo "$(fmt "$total") originals, all local · quit Photos before syncing"
	else
		echo "$(fmt "$total") originals, all local"
	fi
	exit 1
fi

if [ "$recent" -gt 0 ]; then
	warn "$(fmt "$recent") photos added in the last ${GRACE_HOURS}h are still downloading; they'll be copied next time"
fi

if [ "$missing" -gt 0 ]; then
	warn "$(fmt "$missing") originals are only in iCloud or missing, e.g.:"
	sort "$TMP/missing" | head -5 | while IFS= read -r l; do warn "  $l"; done
	if [ "$FORCE" = 0 ]; then
		warn "turn on Photos ▸ Settings ▸ iCloud ▸ Download Originals to this Mac and let it finish, or run the force step in an emergency"
		fail "library incomplete: $(fmt "$missing") of $(fmt "$total") originals missing — backup copy left as it was"
	fi
	warn "forcing the copy anyway; nothing already in the backup will be deleted"
fi

if [ "$photos_running" = 1 ]; then
	[ "$FORCE" = 0 ] && fail "Photos is open — quit it so its database is copied in a consistent state"
	warn "Photos is open; the copied database may be inconsistent (originals are still fine)"
fi

# --- copy -----------------------------------------------------------------

args=(-rtl)
[ "$FORCE" = 0 ] && args+=(--delete)
mkdir -p "$DEST" || fail "cannot create $DEST"

echo "::item copying $(basename "$LIB")"
# progress2 redraws one line with \r: "  1,234  45%  10MB/s  0:01:02 (xfr#…)".
"$RSYNC" "${args[@]}" --info=progress2,stats2 --no-inc-recursive "$LIB/" "$DEST/" 2>"$TMP/rsync.err" |
	awk -v stats="$TMP/stats" 'BEGIN { RS = "[\r\n]" }
		/%/ { for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+%$/) { sub("%", "", $i); print "::progress " $i " 100"; fflush() } next }
		/^Number of regular files transferred|^Total transferred file size/ { print > stats }'
rc=${PIPESTATUS[0]}
while IFS= read -r l; do warn "$l"; done <"$TMP/rsync.err"
[ "$rc" -eq 0 ] || fail "rsync failed (exit $rc): $(tail -1 "$TMP/rsync.err")"

copied=$(awk -F': ' '/files transferred/ { gsub(",", "", $2); print $2 }' "$TMP/stats")
bytes=$(awk -F': ' '/transferred file size/ { gsub(",", "", $2); print $2 + 0 }' "$TMP/stats")

# --- verify ---------------------------------------------------------------

echo "::item verifying"
diffs=$("$RSYNC" "${args[@]}" -n --itemize-changes "$LIB/" "$DEST/" 2>/dev/null | grep -vc '^\.d' || true)
[ "$diffs" -eq 0 ] ||
	fail "copy differs from the library in $(fmt "$diffs") files — did Photos or iCloud change it during the copy? Run again"

summary="$(fmt "$total") originals · $(fmt "${copied:-0}") files copied ($(human "${bytes:-0}")) · verified"
[ "$FORCE" = 1 ] && [ "$missing" -gt 0 ] && summary="$summary · FORCED with $(fmt "$missing") missing"
echo "$summary"
