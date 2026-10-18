"""What to rename, rule-based cleanup, and checks on the model's answers.

The model only ever sees file names that already passed every rule here, and
its answers are checked against the original before being accepted.
"""
import os
import re
import unicodedata

# ── what is off-limits ───────────────────────────────────────────────────

MEDIA_EXT = {
    # video
    "mkv", "mp4", "m4v", "avi", "mov", "wmv", "mpg", "mpeg", "ts", "m2ts", "vob", "webm", "flv", "3gp",
    # audio
    "mp3", "flac", "m4a", "aac", "ogg", "opus", "wav", "aiff", "aif", "wma", "ape", "alac",
    # images
    "jpg", "jpeg", "png", "heic", "heif", "gif", "webp", "tif", "tiff", "bmp",
    "cr2", "cr3", "nef", "arw", "dng", "raf", "orf", "rw2",
    # books and documents people name by title
    "pdf", "epub", "mobi", "djvu", "fb2", "cbz", "cbr",
}
# Files that travel with a video and must keep its name.
COMPANION_EXT = {"srt", "ass", "ssa", "sub", "idx", "vtt", "nfo", "xmp", "aae"}

# A folder containing any of these is a code project: its whole tree is left alone.
PROJECT_MARKERS = {
    ".git", ".hg", ".svn", "package.json", "go.mod", "Cargo.toml", "pyproject.toml", "setup.py",
    "requirements.txt", "pom.xml", "build.gradle", "Gemfile", "composer.json", "Makefile", "CMakeLists.txt",
    ".xcodeproj", "Package.swift", "mix.exs",
}
# Folders that are really documents (macOS packages) or tool-managed trees.
SKIP_DIR_SUFFIX = (".app", ".photoslibrary", ".fcpbundle", ".logicx", ".band", ".imovielibrary", ".lrdata",
                   ".lrcat-data", ".bundle", ".framework", ".xcodeproj", ".xcworkspace", ".pkg", ".plugin",
                   ".kext", ".tvlibrary", ".musiclibrary", ".aplibrary", ".dvdmedia", ".sparsebundle")
SKIP_DIR_NAMES = {".backuptool", "node_modules", "__pycache__", ".Spotlight-V100", ".fseventsd", ".Trashes",
                  ".TemporaryItems", ".DocumentRevisions-V100", "System Volume Information", "$RECYCLE.BIN",
                  "venv", ".venv", "vendor", "Pods", "DerivedData"}

# Names straight out of a camera or phone: other software and sidecar files
# depend on them, and they carry no noise anyway.
CAMERA = re.compile(
    r"^("
    r"IMG|DSC[NF]?|DSCF|_DSC|DCIM|MVI|MOV|VID|PXL|GOPR|GP\d{2}|GH\d{2}|GX\d{2}|DJI|YDXJ|P\d{7}|PANO|"
    r"Screenshot|Screen Shot|Simulator Screen Shot|WhatsApp (Image|Video)|signal-|photo|video|FILE|SAM|CIMG|"
    r"DSCN|IMAG|HPIM|PICT|SDC|MAH\d|C\d{4}|A\d{7}|E\d{7}"
    r")[_\-\s]?[\d_\-\s]*(\(\d+\))?(_\w{1,8})?$"
    r"|^\d{4}[_\-]\d{4}[_\-]\d{6}([_\-]\d+)?$"   # dashcam 2015_0628_074138_001
    r"|^\d{8}[_\-]\d{6}([_\-]\w+)?$"            # 20190612_141530
    r"|^[0-9A-F]{8}(-[0-9A-F]{4}){3}-[0-9A-F]{12}$",  # UUID names (Photos exports)
    re.I,
)


def skip_reason(name):
    """Why a file must not be renamed, or None if it's a candidate."""
    stem, ext = split_ext(name)
    if name.startswith("."):
        return "hidden file"
    if ext.lower() not in MEDIA_EXT and ext.lower() not in COMPANION_EXT:
        return "not a media file"
    if CAMERA.match(stem):
        return "camera name"
    return None


def split_ext(name):
    """('The.Movie.2017.1080p', 'mkv'). Only a short alphanumeric suffix
    counts as an extension, so 'Mr. Robot' keeps its dot."""
    m = re.match(r"^(.*)\.([A-Za-z0-9]{1,5})$", name)
    if m and m.group(1):
        return m.group(1), m.group(2)
    return name, ""


# ── rule-based cleanup ───────────────────────────────────────────────────

# A name containing one of these is a scene/torrent release name. Only then
# are the ambiguous tags below removed: on their own, words like "en",
# "proper" or "extended" are just words.
STRONG = re.compile(
    r"(?<![^\W_])(2160p|1080[pi]|720p|576p|480p|4k|x26[45]|h\.?26[45]|hevc|xvid|divx|"
    r"web-?dl|web-?rip|bdrip|brrip|blu-?ray|bluray|remux|dvdrip|hdtv|hdrip|yts(\.\w+)?|yify|rarbg)(?![^\W_])",
    re.I,
)
RELEASE_ONLY = re.compile(
    r"(?<![^\W_])(aac(2\.0|5\.1)?|ac3|eac3|dts(-hd)?(-ma)?|truehd|atmos|ddp?5\.1|dd5\.1|5\.1|7\.1|2\.0|"
    r"hdr10\+?|hdr|dv|dovi|10bit|8bit|uhd|avc|web|proper|repack|extended|unrated|limited|internal|"
    r"multi|dual(\s?audio)?|subs?|subbed|eng|rus|ukr|ita|ger|fre|spa|eztv|ettv|etrg|axxo|evo|sparks|"
    r"ntb|fgt|ion10|psa|rartv|tgx|galaxytv|mircrew|cc|criterion)(?![^\W_])",
    re.I,
)
# Only endings that don't collide with dotted names: ".tv", ".me", ".cc"
# also appear in "Show.Me.2001.CC" style release names.
TLDS = "com|org|net|ru|ua|info|рф"
URL = re.compile(rf"(?<![^\W_])(www\.)?[^\W\d_][\w-]{{3,}}\.({TLDS})(?![^\W_])", re.I)
SITE_TAG = re.compile(rf"[\[\(]\s*(?:www\.)?[\w-]+\.(?:{TLDS})\s*[\]\)]", re.I)
EMPTY_BRACKETS = re.compile(r"[\[\(\{]\s*[-_.,\s]*\s*[\]\)\}]")
YEAR = re.compile(r"(?<!\d)(19[0-9]{2}|20[0-4][0-9])(?!\d)")
EPISODE = re.compile(r"\bS\d{1,2}E\d{1,3}\b", re.I)


def _identifier_like(s):
    """Mostly digits (Telegram/phone exports, IDs): nothing to tidy."""
    letters = sum(c.isalpha() for c in s)
    digits = sum(c.isdigit() for c in s)
    return digits > letters


COPY_SUFFIX = re.compile(r"\s*\((\d{1,3})\)$")


def rule_clean(stem):
    """Mechanical fixes that need no judgement. Conservative: when in
    doubt, leave the name as it is."""
    s = unicodedata.normalize("NFC", stem).strip()
    # a Finder/browser copy number, "Name (1)": clean the name, keep the number
    m = COPY_SUFFIX.search(s)
    if m and m.start() > 0:
        return f"{rule_clean(s[: m.start()])} ({m.group(1)})"
    if _identifier_like(s):
        return s
    # a media extension left inside the name: "Clip.mp4.mp4"
    s = re.sub(r"\.(" + "|".join(MEDIA_EXT) + r")$", "", s, flags=re.I)
    s = SITE_TAG.sub(" ", s)
    s = URL.sub(" ", s)
    release = bool(STRONG.search(s))
    # Separators instead of spaces: only in names that have no spaces.
    if " " not in s.strip():
        if s.count(".") >= 2:
            s = re.sub(r"(?<=[^\W\d_]{2})\.(?=[^\W_])|(?<=\d)\.(?=[^\W\d_])|(?<=[^\W\d_])\.(?=\d)", " ", s)
        if s.count("_") >= 2:
            s = re.sub(r"^(\d{1,3})-(?=[^\W\d_])", r"\1 - ", s)  # "01-artist_name" → "01 - artist name"
            s = s.replace("_", " ")
        if release and s.count("-") >= 2:
            s = s.replace("-", " ")
    if release:
        # Drop quality tags at the very start ("720p-joyce-…"), then cut at the
        # first tag that follows the title: what comes after is release noise.
        s = re.sub(r"^\s*(" + STRONG.pattern + r")[\s.\-_]*", "", s, flags=re.I)
        m = STRONG.search(s) or RELEASE_ONLY.search(s)
        if m and s[: m.start()].strip(" -_.[("):
            s = s[: m.start()]
        s = RELEASE_ONLY.sub(" ", s)
        s = re.sub(r"(?<=[^\W_])\.(?=[^\W_])(?!\d)|(?<=[^\W\d_])\.(?=\d)", " ", s)  # dots between words, not "5.1"
        s = re.sub(r"[\[(]\s*$", "", s.strip())            # bracket left open by the cut
        s = re.sub(r"\[\s*(19|20)(\d\d)\s*$", r"(\1\2)", s)  # "Harbor [1989" → "Harbor (1989)"
        s = re.sub(r"(?<=[^\W\d_]) (19[0-9]{2}|20[0-4][0-9])$", r" (\1)", s.strip(" -_.,"))  # "Harbor 1989" → "Harbor (1989)"
    s = EMPTY_BRACKETS.sub(" ", s)
    s = re.sub(r"\s+", " ", s).strip(" -_.,")
    return s or stem


# Words in capitals (4+ letters), counted case-sensitively.
SHOUT = re.compile(r"(?<![^\W_])[^\W\d_a-zа-яёіїєґ]{4,}(?![^\W_])")


def needs_model(s):
    """Names the rules can't finish: casing that's broken (shouting or all
    lowercase) or leftover bracket/symbol noise."""
    letters = [c for c in s if c.isalpha()]
    if len(letters) < 4 or _identifier_like(s) or " " not in s.strip():
        return False  # too short, an ID, or a slug like "menu-ost2-button"
    # Caps Lock: nearly every letter a capital. A few capitalised words among
    # normal ones ("Learn to COOK with CONFIDENCE") are deliberate emphasis.
    if sum(c.isupper() for c in letters) / len(letters) >= 0.8 and len(SHOUT.findall(s)) >= 2:
        return True
    words = re.findall(r"[^\W\d_]{3,}", s)
    if len(words) >= 3 and all(w.islower() for w in words):
        return True
    return bool(re.search(r"[\[{][0-9A-F]{6,}[\]}]|\s[-_.]{2,}\s", s))


def needs_cleanup(stem):
    """Anything at all to do for this name?"""
    return rule_clean(stem) != stem or needs_model(stem)


# ── checking the model's answer ──────────────────────────────────────────

BAD_CHARS = re.compile(r'[/\\:*?"<>|\x00-\x1f]')
WORD = re.compile(r"[^\W_]+", re.U)


def _words(s):
    return [w.casefold() for w in WORD.findall(s)]


def validate(original_stem, proposed):
    """Returns (ok, why). The model may drop words and change case, but not
    invent words, drop the year, lose Cyrillic, or produce an unsafe name."""
    p = proposed.strip()
    if not p:
        return False, "empty"
    if p.startswith(".") or "/" in p or ":" in p or any(BAD_CHARS.match(c) and c not in original_stem for c in p):
        return False, "unsafe characters"
    if len(p) > 200:
        return False, "too long"
    orig = set(_words(original_stem))
    # every word in the answer must come from the original (allowing words
    # the original glued together with dots, e.g. "Mr.Robot")
    joined = "".join(_words(original_stem))
    for w in _words(p):
        if w not in orig and w not in joined:
            return False, f"invented word '{w}'"
    years = set(re.findall(r"\b(19\d\d|20\d\d)\b", original_stem))
    if years and not years & set(re.findall(r"\b(19\d\d|20\d\d)\b", p)):
        return False, "dropped the year"
    if re.search(r"[Ѐ-ӿ]", original_stem) and not re.search(r"[Ѐ-ӿ]", p):
        return False, "dropped the Cyrillic title"
    if len(_words(p)) < max(1, len([w for w in _words(original_stem) if not (STRONG.fullmatch(w) or RELEASE_ONLY.fullmatch(w))]) // 4):
        return False, "dropped too much"
    return True, ""


def join_name(stem, ext):
    return f"{stem}.{ext}" if ext else stem


SMALL_EN = {"a", "an", "the", "and", "or", "nor", "but", "of", "in", "on", "at", "to", "for", "by", "with",
            "from", "as", "vs", "into", "onto", "over", "per", "via"}


def polish(name):
    """Lower-cases English small words the model capitalised ("End Of The
    Road" → "End of the Road"), except first words and words that start a
    segment (after " - ", "(" or ": ")."""
    if not name.isascii():
        return name
    out = []
    for i, m in enumerate(re.finditer(r"\S+|\s+", name)):
        w = m.group(0)
        before = "".join(out).rstrip()
        starts_segment = not before or before.endswith(("-", "(", ":", "[", "–", "—"))
        if w.lower() in SMALL_EN and w[:1].isupper() and (len(w) == 1 or w[1:].islower()) and not starts_segment:
            w = w.lower()
        out.append(w)
    return "".join(out)


def title_case(name):
    """Deterministic Title Case for plain-ASCII names that are entirely
    lowercase: the model's fallback, and good enough for English."""
    if not name.isascii():
        return name
    words = re.findall(r"[A-Za-z]{3,}", name)
    if len(words) < 3 or not all(w.islower() for w in words):
        return name
    return polish(re.sub(r"(?<![\w'])([a-z])", lambda m: m.group(1).upper(), name))
