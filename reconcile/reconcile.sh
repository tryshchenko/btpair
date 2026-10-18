#!/bin/bash
# Starts the drive reconciliation UI at http://127.0.0.1:8765/
#
#   ./reconcile.sh            build the UI if needed, start, open the browser
#   ./reconcile.sh --no-open  same, without opening a browser
#
# Env: ATLAS_PORT (8765), ATLAS_INVENTORY (~/drive-inventory),
#      ATLAS_DEV_ROOT (treat subfolders of a directory as drives, for testing)
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -d web/node_modules ]; then
	echo "installing web dependencies…"
	(cd web && npm install --no-fund --no-audit --silent)
fi
if [ ! -f web/dist/index.html ] || [ -n "$(find web/src web/index.html -newer web/dist/index.html 2>/dev/null | head -1)" ]; then
	echo "building web UI…"
	(cd web && npm run build --silent >/dev/null)
fi

open_flag=--open
[ "${1:-}" = "--no-open" ] && open_flag=
exec python3 server/main.py $open_flag
