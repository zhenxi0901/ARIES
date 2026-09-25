"""Wait until a task's own applications answer, inside an ARIES Toolathlon sandbox.

When an occurrence runs its own copies of Toolathlon's applications (Canvas,
poste.io, WooCommerce) beside its sandbox, they start with the sandbox and
some take minutes to boot. Toolathlon's preprocess seeds them through the same
localhost ports its MCP servers use, so this probes those ports, through the
loopback forwarder, at the protocol each server speaks, until every
application has answered three times in a row, and writes the seconds each
took to --out. Standard library only.
"""

import argparse
import json
import socket
import ssl
import sys
import time
import urllib.error
import urllib.request


def http_status(url):
    context = ssl._create_unverified_context()  # Toolathlon's proxy is self-signed
    try:
        with urllib.request.urlopen(url, timeout=10, context=context) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code
    except (OSError, ValueError):
        return None


def imap_greets(port):
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=10) as connection:
            return b"OK" in connection.recv(256)
    except OSError:
        return False


PROBES = {
    # Canvas's login page, over HTTP and through Toolathlon's HTTPS proxy.
    "canvas": lambda: http_status("http://localhost:10001/login") in (200, 302)
    and http_status("https://localhost:20001/login") in (200, 302),
    # poste.io greets IMAP clients only once its mail stack is up.
    "poste": lambda: imap_greets(1143),
    # WooCommerce's REST API refuses an unauthenticated call once it is up.
    "woocommerce": lambda: http_status("http://localhost:10003/store81/wp-json/wc/v3/products") in (200, 401),
}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--apps", required=True, help="comma-separated: canvas, poste, woocommerce")
    parser.add_argument("--timeout", type=float, default=600)
    parser.add_argument("--interval", type=float, default=3)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    apps = [app for app in args.apps.split(",") if app]
    unknown = [app for app in apps if app not in PROBES]
    if unknown:
        raise SystemExit(f"appready: unknown application {', '.join(unknown)}")
    started = time.monotonic()
    streak = dict.fromkeys(apps, 0)
    ready = dict.fromkeys(apps)
    while True:
        for app in apps:
            if ready[app] is None:
                streak[app] = streak[app] + 1 if PROBES[app]() else 0
                if streak[app] >= 3:
                    ready[app] = round(time.monotonic() - started, 1)
        if all(seconds is not None for seconds in ready.values()) or time.monotonic() - started > args.timeout:
            break
        time.sleep(args.interval)
    with open(args.out, "w", encoding="ascii") as out:
        json.dump({"ready_seconds": ready, "timeout_seconds": args.timeout}, out)
    waiting = [app for app, seconds in ready.items() if seconds is None]
    if waiting:
        print(f"appready: not ready after {args.timeout:.0f} s: {', '.join(waiting)}", flush=True)
        sys.exit(1)
    print("appready: " + ", ".join(f"{app} {seconds} s" for app, seconds in ready.items()), flush=True)


if __name__ == "__main__":
    main()
