#!/usr/bin/env python3
"""Drive reconciliation: which files live on which drives.

usage: main.py [--port 8765] [--inventory ~/drive-inventory] [--open]
"""
import argparse
import os
import sys
import threading
import webbrowser

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from atlas.index import Index  # noqa: E402
from atlas.jobs import Jobs  # noqa: E402
from atlas.server import App, serve  # noqa: E402


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--port", type=int, default=int(os.environ.get("ATLAS_PORT", 8765)))
    ap.add_argument("--inventory", default=os.environ.get("ATLAS_INVENTORY", "~/drive-inventory"))
    ap.add_argument("--open", action="store_true", help="open the browser")
    args = ap.parse_args()

    inv = os.path.expanduser(args.inventory)
    os.makedirs(inv, exist_ok=True)
    web = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "web", "dist")
    index = Index(inv)
    app = App(index, Jobs(index, inv), inv, web)

    def initial_sync():  # pick up CSVs changed outside the tool, without delaying startup
        app.syncing = True
        try:
            index.sync()
        finally:
            app.syncing = False

    threading.Thread(target=initial_sync, daemon=True).start()
    httpd = serve(app, args.port)
    url = f"http://127.0.0.1:{args.port}/"
    print(f"reconcile: {url}  (inventory: {inv})", flush=True)
    if args.open:
        webbrowser.open(url)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
