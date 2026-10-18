#!/usr/bin/env python3
"""The original one-off inventory script, kept so tests can check that the
reconcile scanner writes exactly the same CSV. Read-only inventory of a drive.

usage: scan.py /Volumes/NAME [OUT_DIR]

Writes OUT_DIR/<NAME>.csv     one row per file: path,size,mtime,ext
       OUT_DIR/<NAME>.json    drive facts: uuid, filesystem, capacity, scan time
Never writes to the drive itself.
"""
import csv, datetime, json, os, plistlib, subprocess, sys, unicodedata

SKIP_DIRS = {".Spotlight-V100", ".fseventsd", ".Trashes", ".TemporaryItems", ".DocumentRevisions-V100"}

def drive_facts(root):
    info = {}
    try:
        info = plistlib.loads(subprocess.run(["diskutil", "info", "-plist", root], capture_output=True, check=True).stdout)
    except Exception:
        pass
    st = os.statvfs(root)
    return {
        "name": os.path.basename(root.rstrip("/")),
        "mount": root,
        "volume_uuid": info.get("VolumeUUID"),
        "filesystem": info.get("FilesystemName") or info.get("FilesystemType"),
        "device": info.get("DeviceIdentifier"),
        "media": info.get("MediaName"),
        "capacity_bytes": st.f_blocks * st.f_frsize,
        "free_bytes": st.f_bavail * st.f_frsize,
        "scanned_at": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
    }

def walk(root, errors):
    stack = [root]
    while stack:
        d = stack.pop()
        try:
            it = os.scandir(d)
        except OSError as e:
            errors.append(f"{d}: {e.strerror}")
            continue
        with it:
            for e in it:
                try:
                    if e.is_dir(follow_symlinks=False):
                        if not (d == root and (e.name in SKIP_DIRS or e.name.startswith(".Trash-"))):
                            stack.append(e.path)
                    elif e.is_file(follow_symlinks=False):
                        yield e.path, e.stat(follow_symlinks=False)
                except OSError as ex:
                    errors.append(f"{e.path}: {ex.strerror}")

def main():
    root = os.path.abspath(sys.argv[1])
    out = os.path.expanduser(sys.argv[2] if len(sys.argv) > 2 else "~/drive-inventory")
    facts = drive_facts(root)
    errors, n, total = [], 0, 0
    with open(os.path.join(out, facts["name"] + ".csv"), "w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(["path", "size", "mtime", "ext"])
        for p, st in walk(root, errors):
            rel = unicodedata.normalize("NFC", os.path.relpath(p, root))
            ext = os.path.splitext(rel)[1].lower().lstrip(".")
            mt = datetime.datetime.fromtimestamp(st.st_mtime).isoformat(timespec="seconds")
            w.writerow([rel, st.st_size, mt, ext])
            n += 1
            total += st.st_size
    facts.update(files=n, bytes_in_files=total, errors=errors)
    with open(os.path.join(out, facts["name"] + ".json"), "w", encoding="utf-8") as f:
        json.dump(facts, f, indent=2, ensure_ascii=False)
    print(f"{facts['name']}: {n} files, {total/1e9:.1f} GB, {len(errors)} errors")

main()
