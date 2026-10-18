#!/bin/bash
# Tests for apple-photos.sh against a tiny fake Photos library.
set -uo pipefail

STEP="$(cd "$(dirname "$0")" && pwd)/apple-photos.sh"
command -v sqlite3 >/dev/null || { echo "skip: sqlite3 not installed"; exit 0; }

W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
LIB="$W/Photos Library.photoslibrary"
export PHOTOS_LIBRARY="$LIB" BT_MAIN="$W/drive"
export PATH="$W/bin:$PATH"
mkdir -p "$W/bin" "$BT_MAIN"
# Pretend Photos is closed regardless of the machine running the tests.
printf '#!/bin/sh\nexit 1\n' >"$W/bin/pgrep" && chmod +x "$W/bin/pgrep"

fails=0
check() { # name, condition
	if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails + 1)); fi
}

now2001=$(($(date +%s) - 978307200))
old=$((now2001 - 30 * 86400))
recent=$((now2001 - 3600))

reset_library() {
	rm -rf "$LIB"
	mkdir -p "$LIB/database" "$LIB/originals/A" "$LIB/originals/B" "$LIB/resources"
	sqlite3 "$LIB/database/Photos.sqlite" "
		CREATE TABLE ZASSET (Z_PK INTEGER PRIMARY KEY, ZDIRECTORY TEXT, ZFILENAME TEXT,
			ZTRASHEDSTATE INTEGER, ZADDEDDATE REAL, ZDATECREATED REAL,
			ZSAVEDASSETTYPE INTEGER, ZCLOUDBATCHPUBLISHDATE REAL);
		CREATE TABLE ZADDITIONALASSETATTRIBUTES (Z_PK INTEGER PRIMARY KEY, ZASSET INTEGER, ZORIGINALFILENAME TEXT);"
	add A A1.heic "$old" 6
	add B B1.jpeg "$old" 3
	echo thumbs >"$LIB/resources/thumb.jpg"
}

add() { # dir, file, added, savedtype [, publishdate] — registers and creates the file
	sqlite3 "$LIB/database/Photos.sqlite" "
		INSERT INTO ZASSET (ZDIRECTORY, ZFILENAME, ZTRASHEDSTATE, ZADDEDDATE, ZDATECREATED, ZSAVEDASSETTYPE, ZCLOUDBATCHPUBLISHDATE)
		VALUES ('$1', '$2', 0, $3, $3, $4, ${5:-NULL});
		INSERT INTO ZADDITIONALASSETATTRIBUTES (ZASSET, ZORIGINALFILENAME) VALUES (last_insert_rowid(), 'IMG_$2');"
	head -c 2048 /dev/urandom >"$LIB/originals/$1/$2"
}

COPY="$BT_MAIN/Photos/Apple/Photos Library.photoslibrary"

# 1. complete library: plan reports it, run copies and verifies
reset_library
out=$("$STEP" plan); rc=$?
check "plan never skips" '[ $rc -eq 1 ]'
check "plan summary" '[[ $out == "2 originals, all local" ]]'
out=$("$STEP" 2>&1); rc=$?
check "complete library copies" '[ $rc -eq 0 ] && cmp -s "$LIB/originals/A/A1.heic" "$COPY/originals/A/A1.heic" && [ -f "$COPY/database/Photos.sqlite" ] && [ -f "$COPY/resources/thumb.jpg" ]'
check "summary is last line" '[[ $(tail -1 <<<"$out") == "2 originals · "*"files copied"*"· verified" ]]'
size=$(find "$LIB" -type f -exec stat -f %z {} + | awk '{ s += $1 } END { printf "%.1f KB", s / 1000 }')
check "summary counts the copy" '[[ $(tail -1 <<<"$out") == *"· 4 files copied ($size) ·"* ]]'

# 2. an old original missing: refuse and leave the copy alone
add A A2.heic "$old" 6
rm "$LIB/originals/A/A2.heic"
rm "$LIB/originals/B/B1.jpeg"
out=$("$STEP" plan)
check "plan warns" '[[ $out == "⚠ 2 of 3 originals are not on this Mac" ]]'
err=$("$STEP" 2>&1 >"$W/stdout"); rc=$?
check "incomplete library refused" '[ $rc -ne 0 ]'
check "error names the count" '[[ $err == *"library incomplete: 2 of 3 originals missing"* ]]'
check "examples listed" 'grep -q "::warn .*IMG_A2.heic" "$W/stdout"'
check "copy untouched" '[ -f "$COPY/originals/B/B1.jpeg" ]'

# 3. force: copies, but deletes nothing from the backup
echo new >"$LIB/originals/A/A3.heic"
out=$("$STEP" --force 2>&1); rc=$?
check "force succeeds" '[ $rc -eq 0 ]'
check "force copies new files" '[ -f "$COPY/originals/A/A3.heic" ]'
check "force keeps old originals" '[ -f "$COPY/originals/B/B1.jpeg" ]'
check "force is visible in summary" '[[ $(tail -1 <<<"$out") == *"FORCED with 2 missing" ]]'
out=$(PHOTOS_FORCE=1 "$STEP" 2>&1); rc=$?
check "PHOTOS_FORCE=1 works too" '[ $rc -eq 0 ]'

# 4. recent imports and shared albums don't block
reset_library
add B B2.heic "$recent" 6 && rm "$LIB/originals/B/B2.heic"
add A S1.jpeg "$old" 4 "$old" && rm "$LIB/originals/A/S1.jpeg"
out=$("$STEP" 2>&1); rc=$?
check "recent + shared album allowed" '[ $rc -eq 0 ]'
check "recent ones mentioned" '[[ $out == *"1 photos added in the last 24h are still downloading"* ]]'

# 5. a normal run mirrors deletions (photos deleted in Photos)
check "deleted original removed from copy" '[ ! -e "$COPY/originals/A/A3.heic" ] && [ ! -e "$COPY/originals/A/A2.heic" ]'

# 6. unreadable / unfamiliar databases fail clearly
sqlite3 "$LIB/database/Photos.sqlite" "ALTER TABLE ZASSET RENAME COLUMN ZDIRECTORY TO ZDIR;"
err=$("$STEP" 2>&1 >/dev/null); rc=$?
check "schema change detected" '[ $rc -ne 0 ] && [[ $err == *"unfamiliar Photos database layout"* ]]'
err=$(PHOTOS_LIBRARY="$W/nope" "$STEP" 2>&1 >/dev/null); rc=$?
check "missing library" '[ $rc -ne 0 ] && [[ $err == "no Photos library at"* ]]'

# 7. Photos open: refuse normally, warn when forced
reset_library
printf '#!/bin/sh\nexit 0\n' >"$W/bin/pgrep"
err=$("$STEP" 2>&1 >/dev/null); rc=$?
check "open Photos refused" '[ $rc -ne 0 ] && [[ $err == "Photos is open"* ]]'
out=$("$STEP" --force 2>&1); rc=$?
check "open Photos forced" '[ $rc -eq 0 ] && [[ $out == *"may be inconsistent"* ]]'

[ $fails -eq 0 ] && echo "all passed" || { echo "$fails failed"; exit 1; }
