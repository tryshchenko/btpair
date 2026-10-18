"""SQLite index over the inventory CSVs.

The CSVs (and their JSON facts) in the inventory folder are the source of
truth; the index under <inventory>/.atlas/ is a cache rebuilt from them
whenever a CSV changes, so CSVs written by the old scan.py are picked up too.

Two files are "the same file" when their name and size match — the same rule
the original analysis used. It's fast, needs no reading of file contents, and
is right for almost everything except files whose contents change in place
without a size change (VeraCrypt containers, mainly). Each file row carries
kh, a 64-bit hash of (name, size), computed once at import so rebuilding the
derived tables never has to rewrite the files table.
"""
import contextlib
import csv
import glob
import hashlib
import json
import os
import sqlite3
import threading
import time

from .classify import CATEGORIES, classify

csv.field_size_limit(1 << 24)

JUNK = [c for c, (v, _) in CATEGORIES.items() if v == "junk"]
DISPOSABLE = [c for c, (v, _) in CATEGORIES.items() if v in ("junk", "probably-junk", "replaceable")]

SCHEMA_VERSION = "2"

SCHEMA = """
CREATE TABLE IF NOT EXISTS drives (
    id INTEGER PRIMARY KEY,
    stem TEXT UNIQUE NOT NULL,
    name TEXT, uuid TEXT, filesystem TEXT,
    capacity INTEGER, free INTEGER, files INTEGER, bytes INTEGER,
    scanned_at TEXT, errors_total INTEGER,
    pair_id TEXT, role TEXT,
    csv_mtime_ns INTEGER, csv_size INTEGER
);
CREATE TABLE IF NOT EXISTS files (
    drive_id INTEGER NOT NULL,
    path TEXT NOT NULL,
    name TEXT NOT NULL,
    size INTEGER NOT NULL,
    mtime TEXT,
    ext TEXT,
    category TEXT,
    kh INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS files_drive_path ON files(drive_id, path);
CREATE INDEX IF NOT EXISTS files_kh ON files(kh, drive_id);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT);
"""


def key_hash(name, size):
    """Identity of a file across drives: a signed 64-bit hash of name+size."""
    d = hashlib.blake2b(f"{name}\0{size}".encode(), digest_size=8).digest()
    return int.from_bytes(d, "big", signed=True)


def _in(values):
    return "(" + ",".join("?" * len(values)) + ")"


class Index:
    def __init__(self, inv_dir):
        self.inv_dir = inv_dir
        os.makedirs(os.path.join(inv_dir, ".atlas"), exist_ok=True)
        self.db_path = os.path.join(inv_dir, ".atlas", "index.sqlite")
        self.write_lock = threading.Lock()
        self.rebuilding = False
        self._migrate()
        with self.connect() as c:
            c.executescript(SCHEMA)
            c.execute("INSERT OR REPLACE INTO meta VALUES ('schema', ?)", (SCHEMA_VERSION,))
        if not self._has("keys"):
            with self.write_lock:
                self._rebuild()

    def _open(self, **kw):
        c = sqlite3.connect(self.db_path, timeout=60, **kw)
        c.row_factory = sqlite3.Row
        c.execute("PRAGMA journal_mode=WAL")
        c.execute("PRAGMA synchronous=NORMAL")
        return c

    @contextlib.contextmanager
    def connect(self):
        """A connection that commits on success and is always closed."""
        c = self._open()
        try:
            with c:
                yield c
        finally:
            c.close()

    def _migrate(self):
        """The index is only a cache: on a layout change, start over."""
        if not os.path.exists(self.db_path):
            return
        try:
            with self.connect() as c:
                row = c.execute("SELECT v FROM meta WHERE k='schema'").fetchone()
        except sqlite3.DatabaseError:
            row = None
        if row is None or row["v"] != SCHEMA_VERSION:
            for ext in ("", "-wal", "-shm"):
                try:
                    os.remove(self.db_path + ext)
                except OSError:
                    pass

    def _has(self, table):
        with self.connect() as c:
            return c.execute("SELECT 1 FROM sqlite_master WHERE name=?", (table,)).fetchone() is not None

    # ── importing ────────────────────────────────────────────────────────

    def inventory_stems(self):
        out = []
        for js in sorted(glob.glob(os.path.join(self.inv_dir, "*.json"))):
            stem = os.path.basename(js)[:-5]
            if os.path.exists(os.path.join(self.inv_dir, stem + ".csv")):
                out.append(stem)
        return out

    def sync(self):
        """Imports CSVs that changed since last time and forgets drives whose
        CSV is gone. Rebuilds the derived tables when anything changed."""
        changed = False
        with self.write_lock:
            stems = set(self.inventory_stems())
            with self.connect() as c:
                known = {r["stem"]: r for r in c.execute("SELECT stem, id, csv_mtime_ns, csv_size FROM drives")}
                for stem, r in known.items():
                    if stem not in stems:
                        c.execute("DELETE FROM files WHERE drive_id=?", (r["id"],))
                        c.execute("DELETE FROM drives WHERE id=?", (r["id"],))
                        changed = True
            for stem in sorted(stems):
                st = os.stat(os.path.join(self.inv_dir, stem + ".csv"))
                r = known.get(stem)
                if r is None or r["csv_mtime_ns"] != st.st_mtime_ns or r["csv_size"] != st.st_size:
                    self._import(stem)
                    changed = True
            if changed:
                self._rebuild()
        return changed

    def import_drive(self, stem):
        with self.write_lock:
            self._import(stem)

    def rebuild(self):
        with self.write_lock:
            self._rebuild()

    def _import(self, stem):
        with open(os.path.join(self.inv_dir, stem + ".json"), encoding="utf-8") as f:
            facts = json.load(f)
        csv_path = os.path.join(self.inv_dir, stem + ".csv")
        st = os.stat(csv_path)
        bt = facts.get("btpair") or {}
        with self.connect() as c:
            row = c.execute("SELECT id FROM drives WHERE stem=?", (stem,)).fetchone()
            vals = (facts.get("name") or stem, facts.get("volume_uuid"), facts.get("filesystem"),
                    facts.get("capacity_bytes"), facts.get("free_bytes"), facts.get("files"),
                    facts.get("bytes_in_files"), facts.get("scanned_at"),
                    facts.get("errors_total", len(facts.get("errors") or [])),
                    bt.get("pair_id"), bt.get("role"), st.st_mtime_ns, st.st_size)
            if row:
                drive_id = row["id"]
                c.execute("""UPDATE drives SET name=?, uuid=?, filesystem=?, capacity=?, free=?, files=?, bytes=?,
                             scanned_at=?, errors_total=?, pair_id=?, role=?, csv_mtime_ns=?, csv_size=? WHERE id=?""",
                          vals + (drive_id,))
                c.execute("DELETE FROM files WHERE drive_id=?", (drive_id,))
            else:
                drive_id = c.execute("""INSERT INTO drives (name, uuid, filesystem, capacity, free, files, bytes,
                             scanned_at, errors_total, pair_id, role, csv_mtime_ns, csv_size, stem)
                             VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)""", vals + (stem,)).lastrowid

            def rows():
                with open(csv_path, newline="", encoding="utf-8") as f:
                    for r in csv.DictReader(f):
                        p = r["path"]
                        # btpair's metadata and trash are never real copies
                        if p.startswith(".backuptool/"):
                            continue
                        size = int(r["size"] or 0)
                        ext = r.get("ext") or ""
                        name = p.rsplit("/", 1)[-1]
                        yield (drive_id, p, name, size, r.get("mtime"), ext, classify(p, size, ext), key_hash(name, size))

            c.executemany("INSERT INTO files (drive_id, path, name, size, mtime, ext, category, kh) VALUES (?,?,?,?,?,?,?,?)", rows())

    def _rebuild(self):
        """Recomputes everything derived from the files table in one
        transaction, so readers keep seeing the old tables until it's done."""
        self.rebuilding = True
        started = time.time()
        try:
            c = self._open(isolation_level=None)  # we manage the transaction
            try:
                c.execute("BEGIN IMMEDIATE")
                for stmt in REBUILD:
                    c.execute(stmt, ())
                c.execute("INSERT OR REPLACE INTO meta VALUES ('built_at', ?)", (time.strftime("%Y-%m-%dT%H:%M:%S"),))
                c.execute("INSERT OR REPLACE INTO meta VALUES ('build_seconds', ?)", (f"{time.time() - started:.1f}",))
                c.execute("COMMIT")
            except BaseException:
                c.execute("ROLLBACK")
                raise
            finally:
                c.close()
        finally:
            self.rebuilding = False

    # ── queries ──────────────────────────────────────────────────────────

    def drives(self):
        with self.connect() as c:
            rows = c.execute("""SELECT d.*, s.unique_files, s.unique_bytes, s.unique_care_bytes, s.shared_bytes
                                FROM drives d LEFT JOIN drive_stats s ON s.drive_id = d.id ORDER BY lower(d.name)""").fetchall()
            return [dict(r) for r in rows]

    def summary(self):
        with self.connect() as c:
            meta = {r["k"]: r["v"] for r in c.execute("SELECT k, v FROM meta")}
            totals = dict(c.execute(f"""SELECT count(*) AS files, coalesce(sum(size),0) AS bytes FROM keys
                                        WHERE coalesce(category,'') NOT IN {_in(JUNK)}""", JUNK).fetchone())
            dist = [dict(r) for r in c.execute("SELECT * FROM copy_dist ORDER BY ndrives")]
            overlap = [dict(r) for r in c.execute("SELECT * FROM overlap")]
            cats = [dict(r) for r in c.execute("""SELECT coalesce(category,'') AS category, count(*) AS files,
                                                  sum(size) AS bytes FROM keys GROUP BY 1 ORDER BY bytes DESC""")]
            exts = [r["ext"] for r in c.execute("""SELECT ext FROM keys WHERE ext != '' GROUP BY ext
                                                   ORDER BY sum(size) DESC LIMIT 200""")]
        return {"built_at": meta.get("built_at"), "build_seconds": meta.get("build_seconds"),
                "totals": totals, "copies": dist, "overlap": overlap, "categories": cats, "exts": exts}

    def files(self, f, offset=0, limit=100, want_total=True):
        """One row per distinct file (name + size), filtered and sorted.

        f keys: q, search_paths, drives, mode (any|all|only|none), min_size,
        max_size, exts, categories, hide_junk, min_copies, max_copies,
        drive + prefix (only files under a folder of one drive), sort, desc.
        """
        where, args = ["1=1"], []
        if f.get("q"):
            if f.get("search_paths"):
                where.append("k.id IN (SELECT kh FROM files WHERE path LIKE ?)")
            else:
                where.append("k.name LIKE ?")
            args.append("%" + f["q"].replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_") + "%")
            where[-1] = where[-1].replace("LIKE ?", "LIKE ? ESCAPE '\\'")
        ds = [int(d) for d in f.get("drives") or []]
        if ds:
            mode = f.get("mode") or "any"
            on_any = f"k.id IN (SELECT key_id FROM key_drives WHERE drive_id IN {_in(ds)})"
            if mode == "any":
                where.append(on_any)
                args += ds
            elif mode == "all":
                where.append(f"(SELECT count(*) FROM key_drives kd WHERE kd.key_id = k.id AND kd.drive_id IN {_in(ds)}) = ?")
                args += ds + [len(ds)]
            elif mode == "only":
                where.append(on_any)
                where.append(f"NOT EXISTS (SELECT 1 FROM key_drives kd WHERE kd.key_id = k.id AND kd.drive_id NOT IN {_in(ds)})")
                args += ds + ds
            elif mode == "none":
                where.append("NOT " + on_any)
                args += ds
        if f.get("min_size") is not None:
            where.append("k.size >= ?")
            args.append(int(f["min_size"]))
        if f.get("max_size") is not None:
            where.append("k.size <= ?")
            args.append(int(f["max_size"]))
        if f.get("exts"):
            where.append(f"k.ext IN {_in(f['exts'])}")
            args += list(f["exts"])
        if f.get("categories"):
            cats = list(f["categories"])
            cond = f"k.category IN {_in([c for c in cats if c])}" if any(cats) else "0"
            if "" in cats:
                cond = f"({cond} OR k.category IS NULL)"
            where.append(cond)
            args += [c for c in cats if c]
        elif f.get("hide_junk"):
            where.append(f"coalesce(k.category,'') NOT IN {_in(JUNK)}")
            args += JUNK
        if f.get("min_copies"):
            where.append("k.ndrives >= ?")
            args.append(int(f["min_copies"]))
        if f.get("max_copies"):
            where.append("k.ndrives <= ?")
            args.append(int(f["max_copies"]))
        if f.get("drive") is not None and f.get("prefix"):
            pre = f["prefix"].strip("/") + "/"
            where.append("k.id IN (SELECT kh FROM files WHERE drive_id = ? AND path >= ? AND path < ?)")
            args += [int(f["drive"]), pre, pre[:-1] + "0"]

        sort = {"name": "k.name COLLATE NOCASE", "size": "k.size", "ext": "k.ext", "ndrives": "k.ndrives",
                "copies": "k.copies", "newest": "k.newest", "category": "k.category", "sample": "k.sample"}.get(f.get("sort") or "", "k.size")
        order = f"{sort} {'DESC' if f.get('desc', True) else 'ASC'}, k.id"
        cond = " AND ".join(where)
        with self.connect() as c:
            rows = [dict(r) for r in c.execute(
                f"SELECT k.* FROM keys k WHERE {cond} ORDER BY {order} LIMIT ? OFFSET ?", args + [int(limit), int(offset)])]
            for r in rows:
                r["id"] = str(r["id"])  # 64-bit: too big for a JavaScript number
            out = {"rows": rows}
            if want_total:
                t = c.execute(f"SELECT count(*) AS n, coalesce(sum(k.size),0) AS bytes FROM keys k WHERE {cond}", args).fetchone()
                out.update(total=t["n"], total_bytes=t["bytes"])
        return out

    def locations(self, key_id):
        with self.connect() as c:
            return [dict(r) for r in c.execute(
                """SELECT f.drive_id, d.name AS drive, f.path, f.size, f.mtime FROM files f
                   JOIN drives d ON d.id = f.drive_id WHERE f.kh = ? ORDER BY lower(d.name), f.path""", (int(key_id),))]

    def folders(self, drive_id, parent=""):
        """Immediate subfolders of parent on one drive, with how much of each
        exists only on this drive. child is NULL for files directly in parent."""
        parent = parent.strip("/")
        pre = parent + "/" if parent else ""
        n = len(pre)
        cond, args = "f.drive_id = ?", [int(drive_id)]
        if pre:
            cond += " AND f.path >= ? AND f.path < ?"
            args += [pre, pre[:-1] + "0"]
        care = f"coalesce(k.category,'') NOT IN {_in(DISPOSABLE)}"
        sql = f"""
            SELECT child, count(*) AS files, sum(size) AS bytes,
                   sum(CASE WHEN nd = 1 THEN size ELSE 0 END) AS unique_bytes,
                   sum(CASE WHEN nd = 1 THEN 1 ELSE 0 END) AS unique_files,
                   sum(CASE WHEN nd = 1 AND care THEN size ELSE 0 END) AS unique_care_bytes,
                   max(mtime) AS newest
            FROM (SELECT CASE WHEN instr(substr(f.path, {n + 1}), '/') > 0
                              THEN substr(f.path, {n + 1}, instr(substr(f.path, {n + 1}), '/') - 1) END AS child,
                         f.size AS size, f.mtime AS mtime, k.ndrives AS nd, {care} AS care
                  FROM files f JOIN keys k ON k.id = f.kh WHERE {cond})
            GROUP BY child ORDER BY bytes DESC"""
        with self.connect() as c:
            return [dict(r) for r in c.execute(sql, DISPOSABLE + args)]


# Statements run by _rebuild, in order, inside one transaction.
REBUILD = [
    "DROP TABLE IF EXISTS keys",
    "DROP TABLE IF EXISTS key_drives",
    "DROP TABLE IF EXISTS drive_stats",
    "DROP TABLE IF EXISTS overlap",
    "DROP TABLE IF EXISTS copy_dist",
    """CREATE TABLE key_drives (key_id INTEGER, drive_id INTEGER, n INTEGER, bytes INTEGER,
                                PRIMARY KEY (key_id, drive_id)) WITHOUT ROWID""",
    "INSERT INTO key_drives SELECT kh, drive_id, count(*), sum(size) FROM files GROUP BY kh, drive_id",
    "CREATE INDEX key_drives_drive ON key_drives(drive_id, key_id)",
    """CREATE TABLE keys (id INTEGER PRIMARY KEY, name TEXT, size INTEGER, ext TEXT, copies INTEGER,
                          ndrives INTEGER, drives TEXT, newest TEXT, oldest TEXT, sample TEXT, category TEXT)""",
    """INSERT INTO keys (id, name, size, ext, copies, newest, oldest, sample, category)
       SELECT kh, min(name), min(size), max(ext), count(*), max(mtime), min(mtime), min(path), max(category)
       FROM files GROUP BY kh""",
    """UPDATE keys SET ndrives = a.nd, drives = a.ds FROM
       (SELECT key_id, count(*) AS nd, group_concat(drive_id) AS ds
        FROM (SELECT key_id, drive_id FROM key_drives ORDER BY key_id, drive_id) GROUP BY key_id) a
       WHERE a.key_id = keys.id""",
    "CREATE INDEX keys_size ON keys(size)",
    "CREATE INDEX keys_ndrives ON keys(ndrives, size)",
    "CREATE INDEX keys_name ON keys(name COLLATE NOCASE)",
    "CREATE INDEX keys_newest ON keys(newest)",
    "CREATE INDEX keys_ext ON keys(ext)",
    f"""CREATE TABLE drive_stats AS
        SELECT kd.drive_id,
               sum(CASE WHEN k.ndrives = 1 THEN kd.n ELSE 0 END) AS unique_files,
               sum(CASE WHEN k.ndrives = 1 THEN kd.bytes ELSE 0 END) AS unique_bytes,
               sum(CASE WHEN k.ndrives = 1 AND coalesce(k.category,'') NOT IN ({",".join(repr(c) for c in DISPOSABLE)})
                        THEN kd.bytes ELSE 0 END) AS unique_care_bytes,
               sum(CASE WHEN k.ndrives > 1 THEN kd.bytes ELSE 0 END) AS shared_bytes
        FROM key_drives kd JOIN keys k ON k.id = kd.key_id GROUP BY kd.drive_id""",
    """CREATE TABLE overlap AS
       SELECT a.drive_id AS a, b.drive_id AS b, count(*) AS files, sum(k.size) AS bytes
       FROM key_drives a JOIN key_drives b ON a.key_id = b.key_id AND a.drive_id < b.drive_id
       JOIN keys k ON k.id = a.key_id WHERE k.ndrives > 1 GROUP BY a.drive_id, b.drive_id""",
    f"""CREATE TABLE copy_dist AS
        SELECT ndrives, count(*) AS files, sum(size) AS bytes FROM keys
        WHERE coalesce(category,'') NOT IN ({",".join(repr(c) for c in JUNK)}) GROUP BY ndrives""",
]
