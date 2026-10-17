#!/usr/bin/env python3
"""Turns tmux ANSI captures into HTML terminal frames and injects them into
the site template.

    ansi2html.py TEMPLATE SCREENS_DIR OUT_HTML OUT_CSS

The template marks each frame with <!--screen:NAME-->. Styles are collected
into CSS classes shared by all frames.
"""
import html
import re
import sys
from pathlib import Path

SGR = re.compile(r"\x1b\[([0-9;]*)m")
BASIC = ["#1b1b26", "#ff7a90", "#6ee7a8", "#f5c46b", "#a48cff", "#c792ea", "#5fd4f4", "#e6e6ef"]
BRIGHT = ["#6e7185", "#ff9aab", "#94f0c0", "#f8d58f", "#bfadff", "#d9b3f2", "#8fe3f8", "#ffffff"]


def xterm256(n):
    if n < 8:
        return BASIC[n]
    if n < 16:
        return BRIGHT[n - 8]
    if n < 232:
        n -= 16
        steps = [0, 95, 135, 175, 215, 255]
        return "#%02x%02x%02x" % (steps[n // 36], steps[(n // 6) % 6], steps[n % 6])
    g = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (g, g, g)


class Style:
    def __init__(self):
        self.reset()

    def reset(self):
        self.fg = self.bg = None
        self.bold = self.underline = False

    def apply(self, params):
        codes = [int(c) if c else 0 for c in params.split(";")] if params else [0]
        i = 0
        while i < len(codes):
            c = codes[i]
            if c == 0:
                self.reset()
            elif c == 1:
                self.bold = True
            elif c == 22:
                self.bold = False
            elif c == 4:
                self.underline = True
            elif c == 24:
                self.underline = False
            elif c in (38, 48) and i + 1 < len(codes):
                if codes[i + 1] == 2 and i + 4 < len(codes):
                    color = "#%02x%02x%02x" % tuple(codes[i + 2:i + 5])
                    i += 4
                elif codes[i + 1] == 5 and i + 2 < len(codes):
                    color = xterm256(codes[i + 2])
                    i += 2
                else:
                    color = None
                if c == 38:
                    self.fg = color
                else:
                    self.bg = color
            elif c == 39:
                self.fg = None
            elif c == 49:
                self.bg = None
            elif 30 <= c <= 37:
                self.fg = BASIC[c - 30]
            elif 90 <= c <= 97:
                self.fg = BRIGHT[c - 90]
            elif 40 <= c <= 47:
                self.bg = BASIC[c - 40]
            i += 1

    def css(self):
        parts = []
        if self.fg:
            parts.append("color:" + self.fg)
        if self.bg:
            parts.append("background:" + self.bg)
        if self.bold:
            parts.append("font-weight:700")
        if self.underline:
            parts.append("text-decoration:underline")
        return ";".join(parts)


classes = {}


def cells(text):
    """Escapes text and pins symbols the web font may lack (braille spinner,
    check marks, arrows…) to exactly one cell, so columns never drift."""
    out = []
    # Runs of full blocks become one solid box: glyph-per-cell shows
    # hairline seams at fractional pixel positions.
    for run, ch in re.findall(r"(\u2588+)|(.)", text, re.S):
        if run:
            out.append('<b class="blk" style="--n:%d"></b>' % len(run))
            continue
        o = ord(ch)
        if o >= 0x2000 and not (0x2500 <= o <= 0x259F):
            out.append("<i>%s</i>" % html.escape(ch))
        else:
            out.append(html.escape(ch))
    return "".join(out)


def cls(css):
    if css not in classes:
        classes[css] = "t%d" % len(classes)
    return classes[css]


def convert(text):
    style = Style()
    lines = text.rstrip("\n").split("\n")
    # Drop trailing blank rows so frames end at the footer.
    while lines and not SGR.sub("", lines[-1]).strip():
        lines.pop()
    while lines and not SGR.sub("", lines[0]).strip():
        lines.pop(0)
    out = []
    for line in lines:
        runs = []  # (css, text)
        pos = 0
        for m in SGR.finditer(line):
            if m.start() > pos:
                runs.append((style.css(), line[pos:m.start()]))
            style.apply(m.group(1))
            pos = m.end()
        if pos < len(line):
            runs.append((style.css(), line[pos:]))
        # Trim trailing spaces that carry no background.
        while runs and "background" not in runs[-1][0] and runs[-1][1].rstrip(" ") != runs[-1][1]:
            css, t = runs[-1]
            t = t.rstrip(" ")
            runs[-1] = (css, t)
            if not t:
                runs.pop()
            else:
                break
        parts = []
        for css, t in runs:
            t = cells(t)
            parts.append('<span class="%s">%s</span>' % (cls(css), t) if css else t)
        out.append("".join(parts))
    return "\n".join(out), len(lines)


def main():
    template, screens, out_html, out_css = map(Path, sys.argv[1:5])
    page = template.read_text()
    for name in re.findall(r"<!--screen:([a-z0-9-]+)-->", page):
        body, rows = convert((screens / (name + ".ansi")).read_text())
        frame = '<pre class="term-screen" style="--rows:%d">%s</pre>' % (rows, body)
        page = page.replace("<!--screen:%s-->" % name, frame)
    out_html.write_text(page)
    css = "/* generated by scripts/site/ansi2html.py — do not edit */\n"
    css += "\n".join(".term-screen .%s{%s}" % (c, s) for s, c in classes.items()) + "\n"
    out_css.write_text(css)
    print("  wrote %s (%d styles)" % (out_html, len(classes)))


if __name__ == "__main__":
    main()
