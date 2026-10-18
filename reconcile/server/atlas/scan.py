"""Read-only inventory of a mounted drive, in the same format as the
original inventory script (kept as tests/reference_scan.py):

  <stem>.csv   one row per file: path,size,mtime,ext
  <stem>.json  drive facts: name, uuid, filesystem, capacity, scan time, …

The CSV is written to a temporary file and swapped in only when the scan
completes, so a cancelled or failed scan leaves the previous inventory intact.
Nothing is ever written to the drive itself.
"""
import csv
import datetime
import json
import os
import time
import unicodedata

# Never descended into at the drive root: OS bookkeeping, and btpair's own
# metadata (its trash would otherwise look like a backup copy).
SKIP_ROOT_DIRS = {
    ".Spotlight-V100", ".fseventsd", ".Trashes", ".TemporaryItems",
    ".DocumentRevisions-V100", ".backuptool", "System Volume Information",
}
MAX_ERRORS = 1000


class Cancelled(Exception):
    pass


def _skip(root, d, name):
    return d == root and (name in SKIP_ROOT_DIRS or name.startswith(".Trash-"))


def walk(root, errors, cancel=None):
    """Yields (path, stat) for every regular file under root."""
    stack = [root]
    while stack:
        if cancel is not None and cancel.is_set():
            raise Cancelled()
        d = stack.pop()
        try:
            it = os.scandir(d)
        except OSError as e:
            errors.append(f"{os.path.relpath(d, root)}: {e.strerror}")
            continue
        with it:
            for e in it:
                try:
                    if e.is_dir(follow_symlinks=False):
                        if not _skip(root, d, e.name):
                            stack.append(e.path)
                    elif e.is_file(follow_symlinks=False):
                        yield e.path, e.stat(follow_symlinks=False)
                except OSError as ex:
                    errors.append(f"{os.path.relpath(e.path, root)}: {ex.strerror}")


def btpair_identity(root):
    try:
        with open(os.path.join(root, ".backuptool", "identity.json"), encoding="utf-8") as f:
            ident = json.load(f)
        return {"pair_id": ident.get("pair_id"), "role": ident.get("role"), "label": ident.get("label")}
    except (OSError, ValueError):
        return None


def scan(root, facts, out_dir, stem, progress=None, cancel=None):
    """Scans root and writes <stem>.csv/.json into out_dir.

    facts: drive facts gathered by the caller (name, volume_uuid, …).
    progress(files, bytes, current_dir) is called a few times a second.
    Raises Cancelled when cancel (a threading.Event) is set.
    """
    errors, n, total = [], 0, 0
    started = time.monotonic()
    last = 0.0
    csv_path = os.path.join(out_dir, stem + ".csv")
    tmp = csv_path + ".tmp"
    try:
        with open(tmp, "w", newline="", encoding="utf-8") as f:
            w = csv.writer(f)
            w.writerow(["path", "size", "mtime", "ext"])
            for p, st in walk(root, errors, cancel):
                rel = unicodedata.normalize("NFC", os.path.relpath(p, root))
                ext = os.path.splitext(rel)[1].lower().lstrip(".")
                mt = datetime.datetime.fromtimestamp(st.st_mtime).isoformat(timespec="seconds")
                w.writerow([rel, st.st_size, mt, ext])
                n += 1
                total += st.st_size
                if progress and n % 500 == 0:
                    now = time.monotonic()
                    if now - last > 0.25:
                        last = now
                        progress(n, total, os.path.dirname(rel))
        os.replace(tmp, csv_path)
    except BaseException:
        try:
            os.remove(tmp)
        except OSError:
            pass
        raise

    facts = dict(facts)
    facts.update(
        scanned_at=datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        scan_seconds=round(time.monotonic() - started, 1),
        files=n,
        bytes_in_files=total,
        errors=errors[:MAX_ERRORS],
        errors_total=len(errors),
        scanner="reconcile",
    )
    ident = btpair_identity(root)
    if ident:
        facts["btpair"] = ident
    json_path = os.path.join(out_dir, stem + ".json")
    with open(json_path + ".tmp", "w", encoding="utf-8") as f:
        json.dump(facts, f, indent=2, ensure_ascii=False)
    os.replace(json_path + ".tmp", json_path)
    if progress:
        progress(n, total, "")
    return facts
