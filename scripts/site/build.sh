#!/bin/sh
# Rebuilds site/index.html from site/src/index.html with fresh captures of
# the TUI. Usage: scripts/site/build.sh [--no-capture]
set -eu
cd "$(dirname "$0")/../.."
[ "${1:-}" = "--no-capture" ] || ./scripts/site/capture.sh
python3 scripts/site/ansi2html.py site/src/index.html site/build/screens site/index.html site/assets/screens.css
