# btpair — main/replica drive backups

Mirrors a main external drive onto its replica, one way, with a TUI that shows
what is happening and metadata on both drives recording what happened.

```
make build        # → bin/btpair   (needs Homebrew rsync ≥ 3.1: brew install rsync)
make debug        # build fake drives under dev/Volumes
make dev          # run the TUI against them
make test
```

## Tests

`make check` runs gofmt, vet and the full suite under the race detector (~30s);
CI runs the same on macOS. Real rsync runs against temporary folders acting as drives.

- **Safety** (`internal/engine/safety_test.go`):
  - a sync never modifies the main drive
  - the replica becomes an exact mirror, and displaced files land in its trash
  - `.backuptool/` never crosses drives
  - the empty-main, mass-delete, free-space, lock and swapped-drive guards hold
  - dry runs write nothing
  - `paths` and excludes are respected
  - cancellation leaves no partial files
  - interrupted runs are detected and completed
- **Steps**: up-to-date steps are skipped, a failed step doesn't block the sync,
  and pending steps carry over until they succeed. The shell-step protocol and
  environment are covered too.
- **Contracts**: the event stream the UI consumes, the on-disk metadata field
  names, rsync argument safety (direction, no `--delete-excluded`) and pairing rules.
- **TUI**: every screen fits 80–200 columns, the drive bars align, and the key
  flows work, including the cancel confirmation.
- **CLI**: the `btpair` binary end to end: `init`, `status`, `sync` and exit codes.

## How it works

- **Identity lives on the drives.** `btpair init` (or `n` in the TUI) writes
  `.backuptool/identity.json` to both drives: pair ID, role, own volume UUID,
  peer UUID. Pairs and roles are detected from that alone. Cloned metadata, two
  main drives and similar problems block the pair.
- **One-way sync.** `rsync -aHX --delete` from main to replica. During the sync
  the main drive is **remounted read-only** (`diskutil mount readOnly`) and
  verified as read-only before rsync starts. Anything rsync deletes or
  overwrites on the replica is moved to `.backuptool/trash/<run>/`, and the last
  `trash_keep` runs are kept.
- **Guards.** The tool refuses to:
  - sync from an empty main
  - run when the replica lacks free space
  - delete more than `max_delete_percent` of the replica, unless explicitly allowed
- **Metadata.** `.backuptool/` is excluded from rsync:
  - `state.json`: last run status, stats, pending items. It's written as
    `in_progress` before any change, so interrupted runs are detected.
  - `runs/<id>.json`: full record, log and errors of each run.
  - `steps/<id>/`: per-step incremental state, on the main drive.
- **Dry run.** Runs the steps' plans and an rsync `--dry-run`; writes nothing.
- **Checksum verify.** Optional `--checksum` for edits that keep size and mtime.

## Extra steps

Steps run against the main drive before mirroring, so their output reaches the
replica too. You choose them in the picker before each run; selections are
remembered per pair. Each step has:

- `Plan()`: cheap check shown in the picker ("4 repos changed", "up to date").
  Up-to-date steps are skipped.
- `Run()`: does the work and reports progress through a `Reporter`.

Add a Go step by implementing `steps.Step` and calling `steps.Register("type",
factory)` in `init()`. Then reference it from config. Shell steps work today
without recompiling:

```toml
[steps.mystep]
type = "shell"
title = "My step"
default = true
skip_if = "test -f \"$BT_STATE_DIR/done\" && echo 'already done'"   # exit 0 = up to date
run = '''
echo "::progress 1 2"; echo "::item something"   # optional progress protocol
touch "$BT_STATE_DIR/done"
echo "did the thing"                              # last line = result summary
'''
```

The environment provides `BT_MAIN`, `BT_PAIR`, `BT_STATE_DIR` and
`BT_DRY_RUN`. See `dev/config.toml` for more examples.

## Config (`~/.config/btpair/config.toml`, optional)

```toml
[pairs.work]
paths = ["Projects", "Photos"]   # default: whole drive
exclude = ["node_modules/", "*.tmp"]
max_delete_percent = 25
trash_keep = 5
bwlimit = ""                     # e.g. "50M"
steps = ["inventory"]            # default: all configured steps
allow_writable_main = false      # skip the read-only remount (not recommended)
```

## CLI

```
btpair                                   dashboard
btpair status
btpair init --main NAME --replica NAME --pair ID [--label TEXT]
btpair sync PAIR [--dry-run] [--checksum] [--steps a,b | --all-steps] [--allow-deletes]
```

Use `--dev DIR` to treat the subfolders of `DIR` as drives. `BT_ALLOW_DMG=1`
includes mounted disk images, which is useful for testing the real diskutil path.
