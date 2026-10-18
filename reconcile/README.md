# Drive Reconcile

Which files live on which drives. Local web UI over the inventory in
`~/drive-inventory`. A companion to btpair, not part of it. The tools hold no
personal data themselves: file lists, the index and rename plans all live in
`~/drive-inventory`, outside the repository.

```sh
./reconcile.sh            # builds the UI if needed, serves http://127.0.0.1:8765/, opens it
./reconcile.sh --no-open
```

Needs Python 3.11+ (standard library only) and Node 20+ for building the UI.

## What it does

- **Reconcile drives.** Rescans the connected drives you select, in parallel,
  into `~/drive-inventory/<drive>.csv` + `.json`. This is the same format as
  `tools/scan.py`, so old and new inventories mix freely. The scan is
  read-only. It writes to a temp file and swaps it in only when it finishes,
  so a cancelled scan keeps the previous inventory. Skipped: Spotlight,
  fseventsd, Trashes, `.Trash-*`, `System Volume Information`, and btpair's
  `.backuptool` (its trash is not a real copy).
- **Overview.** How much content has 1, 2 or 3+ copies. Per drive: what exists
  only there. A matrix of what each pair of drives shares. Drives can be put
  in groups (the btpair pair name is used by default; stored in
  `~/drive-inventory/atlas-labels.json`).
- **Files.** One row per distinct file, server-side sorted and filtered over
  millions of rows: name or path search, size range, extension, kind, number
  of drives, and drive presence ("on any of", "on all of", "only on", "not
  on"). Click a row to see every copy's location. Filters live in the URL, so
  views can be bookmarked.
- **Folders.** Drill into one drive. Each folder shows its size and how much of
  it exists on no other drive ("only here"). This is the view for deciding
  whether an old HDD still holds anything you care about.

"Only here (content)" leaves out things that are replaceable or disposable:
installers, app bundles, build output, film rips and ROMs. The *Kind* column
says how each file was classified (`server/atlas/classify.py`).

## How "same file" is decided

Same name and same size, on any drive, in any folder. Contents are not
compared. That's fast and right for almost everything. It's wrong for files
changed in place without a size change: VeraCrypt containers are the main
case, where same name and size does not mean same contents.

## Layout

- `server/main.py` starts the HTTP server (127.0.0.1 only; refuses non-localhost
  Host headers).
- `server/atlas/scan.py` is the read-only drive scanner.
- `server/atlas/index.py` is the SQLite index in `~/drive-inventory/.atlas/`.
  It's a cache, rebuilt from the CSVs whenever they change, and safe to delete.
- `server/atlas/jobs.py` runs reconcile jobs in parallel, then batches index
  rebuilds.
- `server/tests/` runs with `python3 -m unittest discover -s server/tests`.
- `web/` is React + AG Grid Community (Vite). `npm run dev` proxies to a
  running server.

Env: `ATLAS_PORT`, `ATLAS_INVENTORY`, `ATLAS_DEV_ROOT` (treat the subfolders
of a directory as drives), `ATLAS_ALLOW_DMG=1` (include mounted disk images).

At 2 million files across 6 drives (synthetic): queries take 0.03–1.8 s, and
re-indexing after a reconcile takes about 25–35 s. The UI keeps working on
the previous index meanwhile.

## File-name cleanup (`renamer/`)

```sh
python3 renamer/renamer.py                  # folder picker → proposes new names
python3 renamer/renamer.py plan FOLDER      # same, for a given folder
python3 renamer/renamer.py apply PLAN.tsv   # rename the rows left marked "yes"
python3 renamer/renamer.py undo APPLIED.jsonl
```

`plan` never renames anything. It writes `~/drive-inventory/renames/plan-*.tsv`
(apply, path, new name, how) for review: set a row to `no`, or edit the new
name, then run `apply`. Every rename is logged, so `undo` can put names back.

- **Never touched:** code projects (any folder with `.git`, `package.json`,
  `go.mod` and similar), non-media files, hidden files and folders, app and
  library packages (`.app`, `.photoslibrary`, …), and camera-original names
  (`IMG_1234`, `DSC_`, `PXL_`, dashcam `2015_0628_…`). It refuses to run on a
  btpair replica; rename on the main drive and the next sync carries the names
  over without copying.
- **Rules first.** Separators become spaces, release tags are cut off, site
  names are removed, a film's year goes in parentheses, and duplicate
  extensions (`.mp4.jpg`) are fixed. Ambiguous words ("en", "proper",
  "extended") are only treated as tags in names that clearly are releases.
- **Then the model**, for names whose casing is broken (Caps Lock or all
  lowercase). It's Qwen3-1.7B running locally through Ollama. Ollama is
  started and stopped by the tool, and the model is unloaded right after.
  Capitals used for emphasis ("Learn to COOK") are left alone. Every answer,
  and every rule result, is checked against the original: no invented or
  transliterated words, no lost year, no lost Cyrillic, no unsafe characters.
  Otherwise the safer name is kept.
- **Memory check.** Before loading the model, it compares free memory
  (free + inactive + purgeable pages) with the model's size × 1.2 + 1.25 GB,
  and refuses to start if it doesn't fit (`--no-model` runs rules only).
- Subtitles and sidecars (`.srt`, `.nfo`, …) follow their video's new name.
  A name never overwrites another file: the new name gets " (2)" instead.

The models were compared on 80 real names from a media library. Qwen3-4B mostly
returned names unchanged, while 1.7B fixed casing. The rule checks caught
1.7B's rare bad answers (e.g. a transliterated Cyrillic title).
`--model qwen3:4b` is available if wanted.
