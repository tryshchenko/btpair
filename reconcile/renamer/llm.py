"""Talks to a local Ollama server, and checks there's memory to run a model.

Nothing leaves the machine: Ollama listens on localhost and the models run
on this Mac.
"""
import json
import os
import re
import shutil
import subprocess
import time
import urllib.error
import urllib.request

HOST = os.environ.get("OLLAMA_HOST_URL", "http://127.0.0.1:11434")
GiB = 1 << 30


# ── memory ───────────────────────────────────────────────────────────────

def memory():
    """(total, available) bytes. Available counts free, inactive,
    speculative and purgeable pages: memory macOS hands out without swapping."""
    total = int(subprocess.run(["sysctl", "-n", "hw.memsize"], capture_output=True, text=True, check=True).stdout)
    out = subprocess.run(["vm_stat"], capture_output=True, text=True, check=True).stdout
    page = int(re.search(r"page size of (\d+) bytes", out).group(1))
    pages = {m.group(1): int(m.group(2)) for m in re.finditer(r"^Pages ([\w ]+?):\s+(\d+)\.", out, re.M)}
    avail = sum(pages.get(k, 0) for k in ("free", "inactive", "speculative", "purgeable")) * page
    return total, avail


def required_memory(model_bytes, ctx_tokens=4096):
    """Weights plus room for the context cache and the runtime: a 20%
    margin on the weights, ~0.25 GiB per 4k tokens, and 1 GiB headroom so
    the rest of the Mac stays responsive."""
    return int(model_bytes * 1.2 + 0.25 * GiB * ctx_tokens / 4096 + GiB)


class NotEnoughMemory(Exception):
    pass


def check_memory(model_bytes, ctx_tokens=4096, already_loaded=0):
    """Raises NotEnoughMemory unless the model fits. already_loaded: bytes
    the same model already occupies in Ollama (reused, not loaded again)."""
    total, avail = memory()
    avail += already_loaded
    need = required_memory(model_bytes, ctx_tokens)
    if total < need + 2 * GiB:
        raise NotEnoughMemory(f"this Mac has {total / GiB:.0f} GB of RAM; the model needs about {need / GiB:.1f} GB "
                              f"plus room for macOS. Use a smaller model (--model).")
    if avail < need:
        raise NotEnoughMemory(f"only {avail / GiB:.1f} GB of memory is free, the model needs about {need / GiB:.1f} GB. "
                              f"Quit some apps (browsers, VMs) and try again, or use a smaller model (--model).")
    return need, avail


# ── server ───────────────────────────────────────────────────────────────

def _req(path, body=None, timeout=600):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(HOST + path, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.load(r)


def running():
    try:
        _req("/api/version", timeout=2)
        return True
    except (urllib.error.URLError, OSError):
        return False


class Server:
    """Starts `ollama serve` if it isn't running, and stops it afterwards
    only if we started it."""

    def __init__(self):
        self.proc = None

    def __enter__(self):
        if running():
            return self
        exe = shutil.which("ollama") or "/opt/homebrew/bin/ollama"
        if not os.path.exists(exe):
            raise RuntimeError("Ollama is not installed — `brew install ollama`")
        self.proc = subprocess.Popen([exe, "serve"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                     env={**os.environ, "OLLAMA_FLASH_ATTENTION": "1"})
        for _ in range(60):
            if running():
                return self
            time.sleep(0.25)
        raise RuntimeError("Ollama did not start")

    def __exit__(self, *exc):
        if self.proc:
            self.proc.terminate()
            try:
                self.proc.wait(10)
            except subprocess.TimeoutExpired:
                self.proc.kill()


def model_size(model):
    """Bytes on disk of an installed model, or None if it isn't installed."""
    for m in _req("/api/tags").get("models", []):
        if m["name"] == model or m["name"] == model + ":latest":
            return m["size"]
    return None


def loaded_size(model):
    """Bytes the model currently occupies in Ollama's memory (0 if not loaded)."""
    try:
        for m in _req("/api/ps", timeout=5).get("models", []):
            if m["name"] == model or m["name"] == model + ":latest":
                return m.get("size", 0)
    except (urllib.error.URLError, OSError):
        pass
    return 0


def pull(model):
    exe = shutil.which("ollama") or "/opt/homebrew/bin/ollama"
    subprocess.run([exe, "pull", model], check=True)


def unload(model):
    try:
        _req("/api/generate", {"model": model, "keep_alive": 0}, timeout=30)
    except (urllib.error.URLError, OSError):
        pass


# ── cleaning ─────────────────────────────────────────────────────────────

SYSTEM = """You fix the casing of file names from a personal media library (films, series, music, books).
Input: a JSON list of {"id", "name"}. Output: {"items": [{"id", "name"}]} with every id, in order.

For each name:
- If words are in ALL CAPS or all lowercase, change them to Title Case.
  Small words (of, the, and, a, in, on, to, de, en, и, в) stay lowercase unless they come first.
  Keep acronyms and abbreviations as they are (USA, BBC, DVD, NT2, A1, S01E02).
- Remove leftover junk only: hash codes like [1A2B3C4D], stray repeated symbols.
- Keep every other word exactly as it is: same spelling, same language, same order. Never translate.
  Never add words. Never merge or split names. If nothing needs fixing, return the name unchanged.

Example input: [{"id":1,"name":"GREEN FIELDS AND RIVERS"},{"id":2,"name":"the quiet harbor (2003)"},{"id":3,"name":"Notes on Glass"}]
Example output: {"items":[{"id":1,"name":"Green Fields and Rivers"},{"id":2,"name":"The Quiet Harbor (2003)"},{"id":3,"name":"Notes on Glass"}]}
"""

SCHEMA = {
    "type": "object",
    "properties": {"items": {"type": "array", "items": {
        "type": "object", "properties": {"id": {"type": "integer"}, "name": {"type": "string"}}, "required": ["id", "name"]}}},
    "required": ["items"],
}


def clean_batch(model, names, ctx_tokens=4096):
    """Asks the model to fix a list of names. Returns a list of the same
    length; an entry is None where the model gave no usable answer."""
    body = {
        "model": model,
        "messages": [{"role": "system", "content": SYSTEM},
                     {"role": "user", "content": json.dumps([{"id": i + 1, "name": n} for i, n in enumerate(names)], ensure_ascii=False)}],
        "format": SCHEMA,
        "stream": False,
        "think": False,
        "keep_alive": "10m",
        "options": {"temperature": 0, "num_ctx": ctx_tokens},
    }
    out = _req("/api/chat", body)
    result = [None] * len(names)
    try:
        for it in json.loads(out["message"]["content"]).get("items", []):
            i = int(it["id"]) - 1
            if 0 <= i < len(names) and isinstance(it.get("name"), str):
                result[i] = it["name"]
    except (KeyError, ValueError, TypeError):
        pass
    return result
