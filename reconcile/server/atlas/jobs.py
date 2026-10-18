"""Reconcile jobs: rescan a drive into the inventory, then refresh the index.

Every drive scans in its own thread (they are separate disks, so they don't
compete for I/O). Finished scans are imported right away; a single worker
then rebuilds the derived tables, batching scans that finish close together.
"""
import glob
import itertools
import json
import os
import threading
import time

from . import scan as scanner

QUEUED, SCANNING, INDEXING, DONE, FAILED, CANCELLED = "queued", "scanning", "indexing", "done", "failed", "cancelled"
ACTIVE = (QUEUED, SCANNING, INDEXING)


def stem_for(inv_dir, name, uuid):
    """The inventory file name for a drive: its volume name, unless that name
    already belongs to a different drive. A renamed drive keeps its file."""
    safe = (name or "drive").replace("/", "_").strip() or "drive"
    for js in glob.glob(os.path.join(inv_dir, "*.json")):
        try:
            with open(js, encoding="utf-8") as f:
                facts = json.load(f)
        except (OSError, ValueError):
            continue
        if uuid and facts.get("volume_uuid") == uuid:
            return os.path.basename(js)[:-5]
    existing = os.path.join(inv_dir, safe + ".json")
    if os.path.exists(existing) and uuid:
        try:
            with open(existing, encoding="utf-8") as f:
                other = json.load(f).get("volume_uuid")
        except (OSError, ValueError):
            other = None
        if other and other != uuid:
            return f"{safe} ({uuid[:8]})"
    return safe


class Job:
    _ids = itertools.count(1)

    def __init__(self, facts):
        self.id = next(self._ids)
        self.facts = facts
        self.name = facts["name"]
        self.mount = facts["mount"]
        self.uuid = facts.get("volume_uuid")
        self.state = QUEUED
        self.files = 0
        self.bytes = 0
        self.current = ""
        self.error = ""
        self.stem = None
        self.started = time.time()
        self.finished = None
        self.cancel = threading.Event()

    def to_dict(self):
        end = self.finished or time.time()
        return {"id": self.id, "name": self.name, "mount": self.mount, "uuid": self.uuid, "state": self.state,
                "files": self.files, "bytes": self.bytes, "current": self.current, "error": self.error,
                "started": self.started, "finished": self.finished, "elapsed": round(end - self.started, 1),
                "capacity": self.facts.get("capacity_bytes"), "free": self.facts.get("free_bytes")}


class Jobs:
    def __init__(self, index, inv_dir, coalesce=1.5):
        self.index = index
        self.inv_dir = inv_dir
        self.coalesce = coalesce
        self.lock = threading.Lock()
        self.jobs = []
        self.waiting = []  # imported, waiting for the next rebuild
        self.wake = threading.Condition(self.lock)
        threading.Thread(target=self._rebuilder, daemon=True, name="rebuilder").start()

    def list(self):
        with self.lock:
            return [j.to_dict() for j in reversed(self.jobs[-50:])]

    def active(self):
        with self.lock:
            return any(j.state in ACTIVE for j in self.jobs)

    def start(self, drives):
        """drives: facts of connected drives (from volumes.connected())."""
        started = []
        with self.lock:
            busy = {j.uuid for j in self.jobs if j.state in ACTIVE}
            for facts in drives:
                if facts.get("volume_uuid") in busy:
                    continue
                job = Job(facts)
                self.jobs.append(job)
                started.append(job)
        for job in started:
            threading.Thread(target=self._run, args=(job,), daemon=True, name=f"scan-{job.name}").start()
        return [j.to_dict() for j in started]

    def cancel(self, job_id):
        with self.lock:
            for j in self.jobs:
                if j.id == job_id and j.state in (QUEUED, SCANNING):
                    j.cancel.set()
                    return True
        return False

    def _finish(self, job, state, error=""):
        with self.lock:
            job.state, job.error, job.finished = state, error, time.time()

    def _run(self, job):
        job.state = SCANNING
        try:
            job.stem = stem_for(self.inv_dir, job.name, job.uuid)

            def progress(n, total, cur):
                job.files, job.bytes, job.current = n, total, cur

            scanner.scan(job.mount, job.facts, self.inv_dir, job.stem, progress, job.cancel)
            job.state, job.current = INDEXING, ""
            self.index.import_drive(job.stem)
        except scanner.Cancelled:
            self._finish(job, CANCELLED)
            return
        except Exception as e:  # report anything; the old inventory is untouched
            self._finish(job, FAILED, f"{type(e).__name__}: {e}")
            return
        with self.lock:
            self.waiting.append(job)
            self.wake.notify()

    def _rebuilder(self):
        while True:
            with self.lock:
                while not self.waiting:
                    self.wake.wait()
            time.sleep(self.coalesce)  # let scans finishing together share one rebuild
            with self.lock:
                batch, self.waiting = self.waiting, []
            try:
                self.index.rebuild()
                err = ""
            except Exception as e:
                err = f"index rebuild failed: {e}"
            for job in batch:
                self._finish(job, FAILED if err else DONE, err)
