"""HTTP API and static file server for the web UI.

Listens on 127.0.0.1 only. Requests whose Host header isn't localhost are
refused, so a web page can't reach the API through DNS rebinding, and writes
require a JSON body, which browsers won't send cross-origin without a CORS
preflight this server never approves.
"""
import json
import mimetypes
import os
import re
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from . import volumes
from .classify import CATEGORIES, VERDICTS


class App:
    def __init__(self, index, jobs, inv_dir, web_dir):
        self.index = index
        self.jobs = jobs
        self.inv_dir = inv_dir
        self.web_dir = web_dir
        self.syncing = False
        self._conn_cache = (0.0, [])
        self._conn_lock = threading.Lock()
        self.labels_path = os.path.join(inv_dir, "atlas-labels.json")

    # connected drives are cached briefly: diskutil is slow-ish
    def connected(self, fresh=False):
        with self._conn_lock:
            at, vols = self._conn_cache
            if fresh or time.time() - at > 4:
                vols = volumes.connected()
                self._conn_cache = (time.time(), vols)
            return vols

    def labels(self):
        try:
            with open(self.labels_path, encoding="utf-8") as f:
                return json.load(f)
        except (OSError, ValueError):
            return {}

    def set_label(self, stem, group, note):
        labels = self.labels()
        labels[stem] = {"group": group.strip(), "note": note.strip()}
        tmp = self.labels_path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(labels, f, indent=2, ensure_ascii=False)
        os.replace(tmp, self.labels_path)

    def state(self, fresh=False):
        conn = {v["volume_uuid"]: v for v in self.connected(fresh)}
        labels = self.labels()
        drives = []
        for d in self.index.drives():
            lab = labels.get(d["stem"], {})
            live = conn.pop(d["uuid"], None) if d["uuid"] else None
            d.update(connected=live is not None, mount=live and live["mount"],
                     group=lab.get("group") or d.get("pair_id") or "", note=lab.get("note", ""))
            if live:  # free space is live; the rest is as of the last scan
                d["free_now"] = live.get("free_bytes")
            drives.append(d)
        for v in conn.values():  # connected, never scanned
            drives.append({"id": None, "stem": None, "name": v["name"], "uuid": v["volume_uuid"], "connected": True,
                           "mount": v["mount"], "filesystem": v["filesystem"], "capacity": v["capacity_bytes"],
                           "free": v["free_bytes"], "free_now": v["free_bytes"], "group": "", "note": ""})
        return {"drives": drives, "jobs": self.jobs.list(),
                "index": {"rebuilding": self.index.rebuilding or self.syncing},
                "categories": {c: {"verdict": v, "about": a} for c, (v, a) in CATEGORIES.items()},
                "verdicts": VERDICTS}

    def reconcile(self, uuids):
        conn = {v["volume_uuid"]: v for v in self.connected(fresh=True)}
        missing = [u for u in uuids if u not in conn]
        return {"started": self.jobs.start([conn[u] for u in uuids if u in conn]), "not_connected": missing}


def _ints(v):
    return [int(x) for x in v.split(",") if x.strip()] if v else []


def _strs(v):
    return [x for x in v.split(",")] if v is not None and v != "" else []


def _bool(v):
    return v in ("1", "true", "yes")


def file_filter(q):
    g = lambda k: q.get(k, [None])[0]
    f = {"q": g("q"), "search_paths": _bool(g("paths")), "drives": _ints(g("drives")), "mode": g("mode"),
         "exts": _strs(g("exts")), "hide_junk": _bool(g("hide_junk")), "sort": g("sort"), "desc": g("dir") != "asc",
         "prefix": g("prefix")}
    if g("categories") is not None:
        f["categories"] = g("categories").split(",")
    for k in ("min_size", "max_size", "min_copies", "max_copies", "drive"):
        if g(k) not in (None, ""):
            f[k] = int(float(g(k)))
    return f


class Handler(BaseHTTPRequestHandler):
    app: App = None
    server_version = "reconcile"

    def log_message(self, *a):  # quiet
        pass

    def _host_ok(self):
        host = (self.headers.get("Host") or "").rsplit(":", 1)[0].strip("[]")
        return host in ("127.0.0.1", "localhost", "::1")

    def _json(self, obj, code=200):
        body = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def _body(self):
        if not (self.headers.get("Content-Type") or "").startswith("application/json"):
            raise ValueError("JSON body required")
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}")

    def do_GET(self):
        if not self._host_ok():
            return self._json({"error": "forbidden"}, 403)
        u = urllib.parse.urlparse(self.path)
        q = urllib.parse.parse_qs(u.query, keep_blank_values=True)
        a = self.app
        try:
            if u.path == "/api/state":
                return self._json(a.state(fresh=q.get("fresh") == ["1"]))
            if u.path == "/api/summary":
                return self._json(a.index.summary())
            if u.path == "/api/files":
                off = int(q.get("offset", ["0"])[0])
                lim = min(int(q.get("limit", ["100"])[0]), 1000)
                return self._json(a.index.files(file_filter(q), off, lim, want_total=off == 0))
            if u.path == "/api/locations":
                return self._json(a.index.locations(int(q["key"][0])))
            if u.path == "/api/folders":
                return self._json(a.index.folders(int(q["drive"][0]), q.get("parent", [""])[0]))
            if u.path.startswith("/api/"):
                return self._json({"error": "not found"}, 404)
            return self._static(u.path)
        except (KeyError, ValueError) as e:
            return self._json({"error": f"bad request: {e}"}, 400)

    def do_POST(self):
        if not self._host_ok():
            return self._json({"error": "forbidden"}, 403)
        a = self.app
        try:
            body = self._body()
            if self.path == "/api/reconcile":
                return self._json(a.reconcile(list(body.get("uuids") or [])))
            m = re.fullmatch(r"/api/jobs/(\d+)/cancel", self.path)
            if m:
                return self._json({"ok": a.jobs.cancel(int(m.group(1)))})
            if self.path == "/api/label":
                a.set_label(body["stem"], body.get("group", ""), body.get("note", ""))
                return self._json({"ok": True})
            return self._json({"error": "not found"}, 404)
        except (KeyError, ValueError) as e:
            return self._json({"error": f"bad request: {e}"}, 400)

    def _static(self, path):
        root = os.path.realpath(self.app.web_dir)
        p = os.path.realpath(os.path.join(root, path.lstrip("/")))
        if not p.startswith(root) or not os.path.isfile(p):
            p = os.path.join(root, "index.html")  # single-page app
        if not os.path.isfile(p):
            return self._json({"error": "web UI not built — run ./reconcile.sh, which builds it"}, 500)
        with open(p, "rb") as f:
            body = f.read()
        self.send_response(200)
        self.send_header("Content-Type", mimetypes.guess_type(p)[0] or "application/octet-stream")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def serve(app, port):
    Handler.app = app
    httpd = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    httpd.daemon_threads = True
    return httpd
