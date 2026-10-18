"""Guesses what kind of file a path is, to separate content you care about
from litter. Started as the classifier of a one-off inventory script, minus
the rules that only made sense for one drive.
"""
import re

# category → (verdict, explanation). Verdicts, most to least disposable:
# junk, probably-junk, replaceable, review, keep.
CATEGORIES = {
    "os-litter": ("junk", "macOS/Windows metadata (._*, .DS_Store, Thumbs.db) — regenerated automatically"),
    "empty": ("junk", "zero-byte files"),
    "installer": ("probably-junk", "installers and disk images (.dmg .pkg .exe .msi .apk …) — re-downloadable"),
    "app-bundle": ("probably-junk", "copied application bundles (.app) — reinstall instead"),
    "dev-artifact": ("probably-junk", "dependency/build folders (node_modules, __pycache__, …) — regenerated from source"),
    "media-release": ("replaceable", "commercial film/series rips — replaceable, but large"),
    "emulation": ("replaceable", "emulators and game ROMs — re-obtainable"),
    "trash": ("review", "inside a Trash / Recycle Bin folder — deleted once, but may hold the only copy"),
    "dashcam": ("review", "dash-cam recordings"),
    "named-junk": ("review", "folders named cleanup / bloatware / old"),
    "encrypted": ("keep", "likely encrypted containers (exact MiB multiples ≥ 1 GiB, no extension) — keep, and know the password"),
}
VERDICTS = ["junk", "probably-junk", "replaceable", "review", "keep"]

OS_LITTER = re.compile(r"(^|/)(\._[^/]*|\.DS_Store|Thumbs\.db|desktop\.ini|Icon\r|\.localized|\.apdisk)$", re.I)
INSTALLER_EXT = {"dmg", "pkg", "mpkg", "exe", "msi", "apk", "deb", "rpm", "xip", "appimage"}
INSTALLER_NAME = re.compile(r"(setup|install(er)?|driver)[^/]*\.(zip|rar|7z|iso|img)$", re.I)
APP = re.compile(r"\.app/")
DEV = re.compile(r"(^|/)(node_modules|__pycache__|\.gradle|DerivedData|Pods|\.idea|\.vs|bower_components|\.sass-cache|\.next|\.venv|venv|target/debug)/")
NAMED = re.compile(r"(^|/)(cleanup|bloatware)\s*/", re.I)
TRASH = re.compile(r"(^|/)(\.Trash-\d+|\.Trashes|\$RECYCLE\.BIN|RECYCLER|\.Trash)/", re.I)
RELEASE = re.compile(r"(1080p|720p|2160p|bdrip|brrip|web-?dl|webrip|dvdrip|hdtv|x264|x265|h\.?264|hevc|\bS\d\dE\d\d\b)", re.I)
MEDIA_DIR = re.compile(r"(^|/)(Mov|Movies|Films|Series|TV Shows)/", re.I)
DASHCAM = re.compile(r"(^|/)CARDV/", re.I)
EMU = re.compile(r"(^|/)(Emulators?|ROMs?)/", re.I)
VIDEO = {"mkv", "avi", "m4v", "mp4", "vob", "mpg", "ts", "wmv"}
CONTAINER_EXT = {"", "vst", "vol", "tc", "hc"}


def classify(path: str, size: int, ext: str):
    """Returns a category name, or None for ordinary content."""
    if OS_LITTER.search(path):
        return "os-litter"
    if size == 0:
        return "empty"
    if size >= 1 << 30 and size % (1 << 20) == 0 and ext in CONTAINER_EXT:
        return "encrypted"
    if TRASH.search(path):
        return "trash"
    if APP.search(path):
        return "app-bundle"
    if DEV.search(path):
        return "dev-artifact"
    if ext in INSTALLER_EXT or INSTALLER_NAME.search(path):
        return "installer"
    if NAMED.search(path):
        return "named-junk"
    if ext in VIDEO and (RELEASE.search(path) or MEDIA_DIR.search(path)):
        return "media-release"
    if DASHCAM.search(path):
        return "dashcam"
    if EMU.search(path):
        return "emulation"
    return None


def verdict(category):
    return CATEGORIES[category][0] if category else "content"
