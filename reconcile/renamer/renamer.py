#!/usr/bin/env python3
"""Tidies up media file names with rules and a small local language model.

  renamer.py plan [FOLDER]   propose new names (opens a folder picker without FOLDER)
  renamer.py apply PLAN      rename the rows you left marked "yes" in PLAN
  renamer.py undo LOG        put names back from the log that apply wrote

Nothing is renamed by `plan`. It writes a plan file (tab-separated: apply,
current path, new name, how) to ~/drive-inventory/renames/ for you to review:
change "yes" to "no", or edit a new name, then run `apply`. Code projects,
camera-original names, non-media files and app/library packages are never
touched. Subtitles and sidecars follow their video.

Options: --model qwen3:1.7b (default)  --no-model (rules only)  --batch 10
"""
import argparse
import csv
import datetime
import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import llm  # noqa: E402
import names  # noqa: E402

DEFAULT_MODEL = "qwen3:1.7b"
OUT_DIR = os.path.expanduser(os.environ.get("RENAMER_OUT", "~/drive-inventory/renames"))


def pick_folder():
    """The native macOS folder picker; None if cancelled."""
    script = 'POSIX path of (choose folder with prompt "Choose a folder whose file names to tidy up")'
    r = subprocess.run(["osascript", "-e", script], capture_output=True, text=True)
    return r.stdout.strip().rstrip("/") or None if r.returncode == 0 else None


def volume_root(path):
    p = os.path.abspath(path)
    while not os.path.ismount(p):
        p = os.path.dirname(p)
    return p


def btpair_role(path):
    try:
        with open(os.path.join(volume_root(path), ".backuptool", "identity.json"), encoding="utf-8") as f:
            return json.load(f).get("role")
    except (OSError, ValueError):
        return None


# ── finding candidates ───────────────────────────────────────────────────

def walk(root):
    """Yields (dir, [file names]) for every folder that may be renamed in,
    pruning code projects, packages and tool-managed folders."""
    for d, dirs, files in os.walk(root):
        if any(m in dirs or m in files for m in names.PROJECT_MARKERS) or any(x.endswith(".xcodeproj") for x in dirs):
            dirs[:] = []  # a code project: leave the whole tree alone
            continue
        dirs[:] = sorted(x for x in dirs if not x.startswith(".") and x not in names.SKIP_DIR_NAMES
                         and not x.lower().endswith(names.SKIP_DIR_SUFFIX))
        yield d, sorted(files)


def collect(root):
    """Returns (candidates, stats). A candidate is a media file whose name
    needs work, plus the companion files (subtitles, sidecars) sharing its name."""
    stats = {"files": 0, "folders": 0}
    cands = []
    for d, files in walk(root):
        stats["folders"] += 1
        stats["files"] += len(files)
        present = set(files)
        for f in files:
            why = names.skip_reason(f)
            stem, ext = names.split_ext(f)
            if why or ext.lower() in names.COMPANION_EXT:
                stats[why or "companion"] = stats.get(why or "companion", 0) + 1
                continue
            if not names.needs_cleanup(stem):
                stats["already tidy"] = stats.get("already tidy", 0) + 1
                continue
            companions = [c for c in present if c != f and c.startswith(stem + ".") and names.split_ext(c)[1].lower() in names.COMPANION_EXT]
            cands.append({"dir": d, "name": f, "stem": stem, "ext": ext, "companions": sorted(companions)})
    return cands, stats


# ── planning ─────────────────────────────────────────────────────────────

def plan(args):
    folder = args.folder or pick_folder()
    if not folder:
        sys.exit("no folder chosen")
    folder = os.path.abspath(os.path.expanduser(folder))
    if not os.path.isdir(folder):
        sys.exit(f"not a folder: {folder}")
    if btpair_role(folder) == "replica":
        sys.exit("this is a btpair replica drive: rename on its main drive instead, and the next sync carries the names over")

    print(f"scanning {folder} …", flush=True)
    cands, stats = collect(folder)
    print(f"{stats['files']:,} files in {stats['folders']:,} folders · {len(cands):,} names to tidy")
    for k, v in sorted(stats.items()):
        if k not in ("files", "folders"):
            print(f"   left alone: {v:,} ({k})")
    if not cands:
        return

    for c in cands:
        c["draft"] = names.rule_clean(c["stem"])
        c["how"] = "rules"
        if not names.validate(c["stem"], c["draft"])[0]:  # rules are checked like the model is
            c["draft"] = c["stem"]
    todo = [c for c in cands if names.needs_model(c["draft"])]

    if todo and not args.no_model:
        model_pass(args, todo)
    for c in cands:  # whatever is still all lowercase (model unsure or off): plain Title Case
        tc = names.title_case(c["draft"])
        if tc != c["draft"] and names.validate(c["stem"], tc)[0]:
            c["draft"], c["how"] = tc, c["how"] if c["how"] == "model" else "rules"

    rows = []
    for c in cands:
        new_stem = c["draft"]
        if new_stem == c["stem"]:
            continue
        rows.append((c, new_stem))
    rows = resolve_collisions(rows)
    write_plan(folder, rows)


def model_pass(args, todo):
    print(f"{len(todo):,} names need the model ({args.model}) for casing …", flush=True)
    with llm.Server():
        size = llm.model_size(args.model)
        if size is None:
            print(f"downloading {args.model} (one time) …", flush=True)
            llm.pull(args.model)
            size = llm.model_size(args.model)
        try:
            need, avail = llm.check_memory(size, already_loaded=llm.loaded_size(args.model))
        except llm.NotEnoughMemory as e:
            sys.exit(f"not starting the model: {e}\n(or run with --no-model for rule-based cleanup only)")
        print(f"memory: {avail / llm.GiB:.1f} GB free, model needs about {need / llm.GiB:.1f} GB", flush=True)
        t0 = time.time()
        rejected = 0
        try:
            for i in range(0, len(todo), args.batch):
                batch = todo[i:i + args.batch]
                answers = llm.clean_batch(args.model, [c["draft"] for c in batch])
                for c, a in zip(batch, answers):
                    if a is None:
                        rejected += 1
                        continue
                    a = names.polish(a.strip())
                    ok, why = names.validate(c["stem"], a)
                    if ok:
                        if a != c["draft"]:
                            c["draft"], c["how"] = a, "model"
                    else:
                        rejected += 1
                done = min(i + args.batch, len(todo))
                rate = done / max(time.time() - t0, 0.01)
                print(f"\r   {done:,}/{len(todo):,} · {rate:.1f}/s · ~{(len(todo) - done) / rate / 60:.0f} min left   ", end="", flush=True)
        finally:
            llm.unload(args.model)  # give the memory back straight away
        print(f"\n   model answers rejected by the checks: {rejected} (those keep the rule-based name)")


def resolve_collisions(rows):
    """Two files must never end up with the same name in a folder, and a
    new name must not take an existing file's name."""
    taken = {}
    for c, _ in rows:
        taken.setdefault(c["dir"], set())
    out = []
    listing = {}
    for c, stem in rows:
        d = c["dir"]
        if d not in listing:
            try:
                listing[d] = {n.casefold() for n in os.listdir(d)}
            except OSError:  # the folder went away while we were working
                listing[d] = None
        existing = listing[d]
        if existing is None or c["name"].casefold() not in existing:
            continue
        base, n = stem, 2
        while True:
            name = names.join_name(stem, c["ext"])
            same_file = name.casefold() == c["name"].casefold()  # a case-only change
            if same_file or (name.casefold() not in existing and name.casefold() not in taken[d]):
                break
            stem = f"{base} ({n})"
            n += 1
        taken[d].add(name.casefold())
        out.append((c, stem))
    return out


def write_plan(folder, rows):
    os.makedirs(OUT_DIR, exist_ok=True)
    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    label = os.path.basename(folder.rstrip("/")) or "root"
    path = os.path.join(OUT_DIR, f"plan-{label}-{stamp}.tsv")
    with open(path, "w", newline="", encoding="utf-8") as f:
        w = csv.writer(f, delimiter="\t")
        w.writerow(["apply", "path", "new_name", "how"])
        for c, stem in sorted(rows, key=lambda r: os.path.join(r[0]["dir"], r[0]["name"])):
            w.writerow(["yes", os.path.join(c["dir"], c["name"]), names.join_name(stem, c["ext"]), c["how"]])
            for comp in c["companions"]:
                w.writerow(["yes", os.path.join(c["dir"], comp), stem + comp[len(c["stem"]):], "follows video"])
    by = {}
    for c, _ in rows:
        by[c["how"]] = by.get(c["how"], 0) + 1
    print(f"\n{len(rows):,} renames proposed ({', '.join(f'{v:,} by {k}' for k, v in by.items())})")
    print(f"review: {path}")
    print("set a row's first column to 'no' to skip it, or edit new_name; then:")
    print(f"  {sys.argv[0]} apply '{path}'")
    if sys.stdout.isatty():
        subprocess.run(["open", "-e", path])  # TextEdit; any editor works


# ── applying and undoing ─────────────────────────────────────────────────

def apply(args):
    with open(args.plan, newline="", encoding="utf-8") as f:
        rows = [r for r in csv.DictReader(f, delimiter="\t") if r["apply"].strip().lower() in ("yes", "y")]
    if not rows:
        sys.exit("nothing marked yes")
    if btpair_role(rows[0]["path"]) == "replica":
        sys.exit("this is a btpair replica drive: rename on its main drive instead")
    log_path = args.plan.replace("plan-", "applied-").removesuffix(".tsv") + ".jsonl"
    done = skipped = 0
    with open(log_path, "a", encoding="utf-8") as log:
        for r in rows:
            src = r["path"]
            new = r["new_name"].strip()
            dst = os.path.join(os.path.dirname(src), new)
            problem = None  # names you edited in the plan are your call: no re-checking
            if not new or "/" in new or ":" in new:
                problem = "invalid new name"
            elif not os.path.exists(src):
                problem = "file no longer there"
            elif os.path.exists(dst) and os.path.basename(src).casefold() != new.casefold():
                problem = "a file with the new name exists"
            if problem:
                print(f"skip: {src} ({problem})")
                skipped += 1
                continue
            try:
                if os.path.basename(src).casefold() == new.casefold():
                    tmp = dst + ".renaming"  # case-only change on a case-insensitive disk
                    os.rename(src, tmp)
                    os.rename(tmp, dst)
                else:
                    os.rename(src, dst)
            except OSError as e:
                print(f"skip: {src} ({e.strerror})")
                skipped += 1
                continue
            log.write(json.dumps({"from": src, "to": dst}, ensure_ascii=False) + "\n")
            log.flush()
            done += 1
    print(f"renamed {done:,}, skipped {skipped:,} · undo with: {sys.argv[0]} undo '{log_path}'")


def undo(args):
    with open(args.log, encoding="utf-8") as f:
        entries = [json.loads(line) for line in f if line.strip()]
    back = skipped = 0
    for e in reversed(entries):
        if os.path.exists(e["to"]) and not os.path.exists(e["from"]) or e["from"].casefold() == e["to"].casefold():
            try:
                os.rename(e["to"], e["from"])
                back += 1
                continue
            except OSError:
                pass
        print(f"skip: {e['to']}")
        skipped += 1
    print(f"restored {back:,} names, skipped {skipped:,}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd")
    p = sub.add_parser("plan", help="propose new names")
    p.add_argument("folder", nargs="?")
    p.add_argument("--model", default=DEFAULT_MODEL)
    p.add_argument("--no-model", action="store_true", help="rule-based cleanup only")
    p.add_argument("--batch", type=int, default=10)
    a = sub.add_parser("apply", help="rename what a reviewed plan says")
    a.add_argument("plan")
    u = sub.add_parser("undo", help="put names back")
    u.add_argument("log")
    args = ap.parse_args()
    if args.cmd is None:  # bare `renamer.py`: plan with the folder picker
        args = ap.parse_args(["plan"])
    {"plan": plan, "apply": apply, "undo": undo}[args.cmd](args)


if __name__ == "__main__":
    main()
