import csv
import http.client
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from atlas import jobs as jobsmod  # noqa: E402
from atlas import scan as scanner  # noqa: E402
from atlas import volumes  # noqa: E402
from atlas.classify import classify  # noqa: E402
from atlas.index import Index  # noqa: E402
from atlas.server import App, serve  # noqa: E402

OLD_SCAN = os.path.join(os.path.dirname(os.path.abspath(__file__)), "reference_scan.py")
T0 = 1_700_000_000


def write(root, files):
    for p, content in files.items():
        full = os.path.join(root, p)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "wb") as f:
            f.write(content if isinstance(content, bytes) else content.encode())
        os.utime(full, (T0, T0))


def read_csv(path):
    with open(path, newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def facts(name, root, uuid):
    return {"name": name, "mount": root, "volume_uuid": uuid, "filesystem": "test", "capacity_bytes": 10**9, "free_bytes": 10**8}


class Tmp(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.inv = os.path.join(self.tmp, "inventory")
        self.vols = os.path.join(self.tmp, "Volumes")
        os.makedirs(self.inv)
        os.makedirs(self.vols)

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def drive(self, name, files):
        root = os.path.join(self.vols, name)
        os.makedirs(root, exist_ok=True)
        write(root, files)
        return root

    def scan(self, name, files, uuid=None):
        root = self.drive(name, files)
        return scanner.scan(root, facts(name, root, uuid or "UUID-" + name), self.inv, name)


class ScanTest(Tmp):
    TREE = {
        "Photos/2014/IMG_1.jpg": "a" * 10,
        "Photos/2014/Мама.jpg": "b" * 20,
        "Café.txt": "nfd name",  # decomposed é, stored NFC
        "Docs/report.PDF": "pdf",
        ".DS_Store": "x",
        ".Spotlight-V100/store.db": "skip",
        ".Trash-1000/files/old.txt": "skip",
    }

    def test_format_matches_original_scanner(self):
        root = self.drive("DRIVE", self.TREE)
        scanner.scan(root, facts("DRIVE", root, "U1"), self.inv, "DRIVE")
        new = read_csv(os.path.join(self.inv, "DRIVE.csv"))
        self.assertEqual(list(new[0].keys()), ["path", "size", "mtime", "ext"])
        paths = {r["path"] for r in new}
        self.assertIn("Café.txt", paths)
        self.assertIn("Docs/report.PDF", paths)
        self.assertFalse(any(p.startswith((".Spotlight", ".Trash")) for p in paths))
        self.assertEqual(next(r for r in new if r["path"] == "Docs/report.PDF")["ext"], "pdf")

        if not os.path.exists(OLD_SCAN):
            self.skipTest("original scan.py not present")
        old_out = os.path.join(self.tmp, "old")
        os.makedirs(old_out)
        subprocess.run([sys.executable, OLD_SCAN, root, old_out], check=True, capture_output=True)
        old = read_csv(os.path.join(old_out, "DRIVE.csv"))
        key = lambda rows: sorted(tuple(r.values()) for r in rows)
        self.assertEqual(key(new), key(old))

    def test_skips_btpair_metadata(self):
        self.scan("D", {"a.txt": "a", ".backuptool/trash/20260101/x.mkv": "trashed", ".backuptool/identity.json": "{}"})
        self.assertEqual([r["path"] for r in read_csv(os.path.join(self.inv, "D.csv"))], ["a.txt"])

    def test_records_btpair_pair(self):
        root = self.drive("P", {"a.txt": "a"})
        write(root, {".backuptool/identity.json": json.dumps({"pair_id": "content", "role": "replica"})})
        f = scanner.scan(root, facts("P", root, "U"), self.inv, "P")
        self.assertEqual(f["btpair"]["pair_id"], "content")

    def test_cancel_keeps_previous_inventory(self):
        self.scan("D", {"old.txt": "o"})
        before = open(os.path.join(self.inv, "D.csv")).read()
        write(os.path.join(self.vols, "D"), {f"many/{i}.txt": "x" for i in range(2000)})
        ev = threading.Event()
        ev.set()
        with self.assertRaises(scanner.Cancelled):
            scanner.scan(os.path.join(self.vols, "D"), facts("D", "", "U"), self.inv, "D", cancel=ev)
        self.assertEqual(open(os.path.join(self.inv, "D.csv")).read(), before)
        self.assertFalse(os.path.exists(os.path.join(self.inv, "D.csv.tmp")))


class StemTest(Tmp):
    def test_names(self):
        self.assertEqual(jobsmod.stem_for(self.inv, "Untitled", "U1"), "Untitled")
        self.scan("Untitled", {"a": "a"}, uuid="AAAAAAAA-1")
        # same drive → same file; another drive with the same name → suffixed
        self.assertEqual(jobsmod.stem_for(self.inv, "Untitled", "AAAAAAAA-1"), "Untitled")
        self.assertEqual(jobsmod.stem_for(self.inv, "Untitled", "BBBBBBBB-2"), "Untitled (BBBBBBBB)")
        # a renamed drive keeps its inventory file
        self.assertEqual(jobsmod.stem_for(self.inv, "Holiday SSD", "AAAAAAAA-1"), "Untitled")
        self.assertEqual(jobsmod.stem_for(self.inv, "a/b", "C"), "a_b")


class ClassifyTest(unittest.TestCase):
    def test_categories(self):
        cases = {
            ("x/._foo", 10, ""): "os-litter",
            ("a/.DS_Store", 6, ""): "os-litter",
            ("a/empty.txt", 0, "txt"): "empty",
            ("vault", 5 << 30, ""): "encrypted",
            ("Setup/app.dmg", 100, "dmg"): "installer",
            ("$RECYCLE.BIN/S-1/x.jpg", 5, "jpg"): "trash",
            ("Movies/Film.2017.1080p.mkv", 9, "mkv"): "media-release",
            ("proj/node_modules/x/i.js", 9, "js"): "dev-artifact",
            ("Photos/IMG_1.jpg", 9, "jpg"): None,
        }
        for (p, s, e), want in cases.items():
            self.assertEqual(classify(p, s, e), want, p)


class IndexTest(Tmp):
    def setUp(self):
        super().setUp()
        # A: main archive; B: partial backup of A; HDD: old disk with one unique folder
        self.scan("A", {"Photos/1.jpg": "1" * 100, "Photos/2.jpg": "2" * 200, "Docs/cv.pdf": "c" * 50, "only-a.txt": "a" * 7, ".DS_Store": "x"})
        self.scan("B", {"Backup/Photos/1.jpg": "1" * 100, "Backup/Docs/cv.pdf": "c" * 50, ".backuptool/trash/r/2.jpg": "2" * 200})
        self.scan("HDD", {"Old/Photos/1.jpg": "1" * 100, "Old/Unique/letter.doc": "l" * 1000, "Old/Unique/sub/x.jpg": "x" * 300,
                          "Installers/tool.dmg": "d" * 5000})
        self.idx = Index(self.inv)
        self.idx.sync()
        self.ids = {d["name"]: d["id"] for d in self.idx.drives()}

    def names(self, **f):
        return sorted(r["name"] for r in self.idx.files(f, limit=1000)["rows"])

    def test_copies_across_drives(self):
        rows = {r["name"]: r for r in self.idx.files({}, limit=100)["rows"]}
        self.assertEqual(rows["1.jpg"]["ndrives"], 3)
        self.assertEqual(rows["cv.pdf"]["ndrives"], 2)
        # btpair trash on B is not a copy
        self.assertEqual(rows["2.jpg"]["ndrives"], 1)
        self.assertEqual(sorted(int(x) for x in rows["1.jpg"]["drives"].split(",")), sorted(self.ids.values()))

    def test_drive_filters(self):
        a, b, h = self.ids["A"], self.ids["B"], self.ids["HDD"]
        self.assertEqual(self.names(drives=[h], mode="only"), ["letter.doc", "tool.dmg", "x.jpg"])
        self.assertEqual(self.names(drives=[a, b], mode="all"), ["1.jpg", "cv.pdf"])
        self.assertEqual(self.names(drives=[a], mode="none"), ["letter.doc", "tool.dmg", "x.jpg"])
        self.assertEqual(self.names(drives=[b], mode="any"), ["1.jpg", "cv.pdf"])
        self.assertEqual(self.names(drives=[a, b], mode="only"), [".DS_Store", "2.jpg", "cv.pdf", "only-a.txt"])

    def test_other_filters_and_sort(self):
        self.assertEqual(self.names(min_size=200, max_size=1000), ["2.jpg", "letter.doc", "x.jpg"])
        self.assertEqual(self.names(exts=["jpg"]), ["1.jpg", "2.jpg", "x.jpg"])
        self.assertEqual(self.names(q="LET"), ["letter.doc"])
        self.assertEqual(self.names(q="unique", search_paths=True), ["letter.doc", "x.jpg"])
        self.assertEqual(self.names(q="100%"), [])  # LIKE wildcards are literal
        self.assertNotIn(".DS_Store", self.names(hide_junk=True))
        self.assertEqual(self.names(categories=["installer"]), ["tool.dmg"])
        self.assertNotIn("tool.dmg", self.names(categories=[""]))
        self.assertEqual(self.names(min_copies=2), ["1.jpg", "cv.pdf"])
        self.assertEqual(self.names(max_copies=1, drive=self.ids["HDD"], prefix="Old/Unique"), ["letter.doc", "x.jpg"])
        rows = self.idx.files({"sort": "size", "desc": False}, limit=3)["rows"]
        self.assertEqual([r["size"] for r in rows], [1, 7, 50])
        res = self.idx.files({"min_copies": 2})
        self.assertEqual((res["total"], res["total_bytes"]), (2, 150))
        page = self.idx.files({"sort": "name", "desc": False}, offset=2, limit=2, want_total=False)
        self.assertEqual(len(page["rows"]), 2)
        self.assertNotIn("total", page)

    def test_locations(self):
        key = next(r for r in self.idx.files({"q": "1.jpg"})["rows"])["id"]
        locs = self.idx.locations(key)
        self.assertEqual(sorted(l["path"] for l in locs), ["Backup/Photos/1.jpg", "Old/Photos/1.jpg", "Photos/1.jpg"])

    def test_folders_show_unique_content(self):
        rows = {r["child"]: r for r in self.idx.folders(self.ids["HDD"], "Old")}
        self.assertEqual(rows["Unique"]["unique_bytes"], 1300)
        self.assertEqual(rows["Photos"]["unique_bytes"], 0)
        root = {r["child"]: r for r in self.idx.folders(self.ids["HDD"])}
        self.assertEqual(root["Installers"]["unique_bytes"], 5000)
        self.assertEqual(root["Installers"]["unique_care_bytes"], 0)  # installers are disposable
        top = {r["child"]: r for r in self.idx.folders(self.ids["A"])}
        self.assertIn(None, top)  # files directly at the root
        self.assertEqual(top[None]["files"], 2)

    def test_drive_stats_and_summary(self):
        d = {x["name"]: x for x in self.idx.drives()}
        self.assertEqual(d["HDD"]["unique_bytes"], 6300)
        self.assertEqual(d["HDD"]["unique_care_bytes"], 1300)
        s = self.idx.summary()
        pair = {(o["a"], o["b"]): o["bytes"] for o in s["overlap"]}
        a, b = sorted([self.ids["A"], self.ids["B"]])
        self.assertEqual(pair[(a, b)], 150)
        dist = {c["ndrives"]: c["files"] for c in s["copies"]}
        self.assertEqual(dist, {1: 5, 2: 1, 3: 1})  # 2.jpg only-a.txt letter.doc x.jpg tool.dmg; .DS_Store is junk
        self.assertIn("jpg", s["exts"])

    def test_sync_follows_csv_changes(self):
        self.assertFalse(self.idx.sync())  # nothing changed
        time.sleep(0.01)
        self.scan("B", {"Backup/Photos/1.jpg": "1" * 100, "Backup/Unique/letter.doc": "l" * 1000})
        self.assertTrue(self.idx.sync())
        self.assertEqual(self.names(drives=[self.ids["HDD"]], mode="only"), ["tool.dmg", "x.jpg"])
        os.remove(os.path.join(self.inv, "B.csv"))
        self.idx.sync()
        self.assertNotIn("B", [x["name"] for x in self.idx.drives()])


class JobsTest(Tmp):
    def test_concurrent_reconcile_and_cancel(self):
        idx = Index(self.inv)
        jobs = jobsmod.Jobs(idx, self.inv, coalesce=0.1)
        r1 = self.drive("One", {"a.txt": "a", "shared.bin": "s" * 64})
        r2 = self.drive("Two", {"b.txt": "b", "shared.bin": "s" * 64})
        started = jobs.start([facts("One", r1, "U1"), facts("Two", r2, "U2")])
        self.assertEqual(len(started), 2)
        self.assertEqual(jobs.start([facts("One", r1, "U1")]), [])  # already running
        deadline = time.time() + 20
        while jobs.active() and time.time() < deadline:
            time.sleep(0.05)
        self.assertEqual({j["state"] for j in jobs.list()}, {"done"})
        shared = idx.files({"q": "shared"})["rows"][0]
        self.assertEqual(shared["ndrives"], 2)

        big = self.drive("Big", {f"d{i}/f{j}.txt": "x" for i in range(200) for j in range(20)})
        job = jobs.start([facts("Big", big, "U3")])[0]
        jobs.cancel(job["id"])
        while jobs.active():
            time.sleep(0.05)
        state = next(j for j in jobs.list() if j["id"] == job["id"])["state"]
        self.assertIn(state, ("cancelled", "done"))  # it may finish before the cancel lands
        if state == "cancelled":
            self.assertFalse(os.path.exists(os.path.join(self.inv, "Big.csv")))

    def test_failure_is_reported(self):
        idx = Index(self.inv)
        jobs = jobsmod.Jobs(idx, self.inv, coalesce=0.1)
        jobs.start([facts("Gone", os.path.join(self.vols, "missing"), "U9")])
        while jobs.active():
            time.sleep(0.05)
        # an unreadable root yields an empty scan rather than a crash
        self.assertIn(jobs.list()[0]["state"], ("done", "failed"))


class ServerTest(Tmp):
    def setUp(self):
        super().setUp()
        os.environ["ATLAS_DEV_ROOT"] = self.vols
        self.drive("A", {"x.txt": "x"})
        self.drive("Fresh", {"y.txt": "y"})
        uuid = next(v["volume_uuid"] for v in volumes.connected() if v["name"] == "A")
        self.scan("A", {}, uuid=uuid)
        self.scan("Offline", {"z.txt": "z"})
        shutil.rmtree(os.path.join(self.vols, "Offline"))
        idx = Index(self.inv)
        idx.sync()
        web = os.path.join(self.tmp, "web")
        write(web, {"index.html": "<html>ui</html>"})
        self.httpd = serve(App(idx, jobsmod.Jobs(idx, self.inv), self.inv, web), 0)
        self.port = self.httpd.server_address[1]
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()

    def tearDown(self):
        self.httpd.shutdown()
        os.environ.pop("ATLAS_DEV_ROOT", None)
        super().tearDown()

    def req(self, method, path, body=None, headers=None):
        c = http.client.HTTPConnection("127.0.0.1", self.port)
        c.request(method, path, body=body, headers=headers or {})
        r = c.getresponse()
        data = r.read()
        c.close()
        return r.status, data

    def test_api_and_guards(self):
        st, body = self.req("GET", "/api/state")
        self.assertEqual(st, 200)
        state = json.loads(body)
        d = {x["name"]: x for x in state["drives"]}
        self.assertTrue(d["A"]["connected"])
        self.assertFalse(d["Offline"]["connected"])
        self.assertTrue(d["Fresh"]["connected"])
        self.assertIsNone(d["Fresh"]["id"])  # connected, never scanned
        st, body = self.req("GET", "/api/files?hide_junk=1&sort=name&dir=asc")
        self.assertEqual(json.loads(body)["total"], 2)
        st, body = self.req("POST", "/api/reconcile", body=json.dumps({"uuids": [d["Fresh"]["uuid"], "nope"]}),
                            headers={"Content-Type": "application/json"})
        res = json.loads(body)
        self.assertEqual((len(res["started"]), res["not_connected"]), (1, ["nope"]))
        st, body = self.req("GET", "/")
        self.assertIn(b"ui", body)
        st, _ = self.req("GET", "/api/state", headers={"Host": "evil.example:80"})
        self.assertEqual(st, 403)
        st, _ = self.req("POST", "/api/reconcile", body="uuids=x", headers={"Content-Type": "application/x-www-form-urlencoded"})
        self.assertEqual(st, 400)
        st, _ = self.req("GET", "/api/folders")
        self.assertEqual(st, 400)


if __name__ == "__main__":
    unittest.main()
