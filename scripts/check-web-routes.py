#!/usr/bin/env python3
"""Exact, fail-closed assertion of a BUILT Next route-manifest rewrites.

This is the production gate for the panel API base URL baked into the web image.
Next evaluates `rewrites()` at `next build` time and writes the result into
`web/.next/routes-manifest.json`. If the web image is built with the wrong
(or default localhost) API base, the panel hostname is baked into server routes
(install-agent.sh, webhooks, /api, /ws) and the deployment is broken.

Required exact rewrite mappings (panel reachable at http://panel:8080):
    /api/:path*          -> http://panel:8080/api/:path*
    /ws/:path*           -> http://panel:8080/ws/:path*
    /install-agent.sh    -> http://panel:8080/install-agent.sh
    /webhooks/:path*     -> http://panel:8080/webhooks/:path*

The check inspects ALL rewrite buckets (beforeFiles, afterFiles, fallback) and
FAILS if:
  * the manifest is absent (default; use --allow-missing for local gating),
  * a required source mapping is missing or its destination is wrong,
  * any destination resolves to a loopback address (localhost / 127.0.0.1),
  * the manifest is malformed / missing the rewrites structure.

It does NOT replace a Docker image build, and it does NOT build the web UI
itself; it only asserts the rewrites in a PROVIDED manifest. Run it against a
FRESH host build (`make web-routes-build-check`) to avoid asserting a STALE
prebuilt `web/.next/routes-manifest.json`. Against an existing manifest it may
inspect stale output if the web UI was not rebuilt.

Usage:
    python3 scripts/check-web-routes.py [--manifest PATH] [--allow-missing]
"""
import argparse
import json
import os
import sys

EXPECTED = {
    "/api/:path*": "http://panel:8080/api/:path*",
    "/ws/:path*": "http://panel:8080/ws/:path*",
    "/install-agent.sh": "http://panel:8080/install-agent.sh",
    "/webhooks/:path*": "http://panel:8080/webhooks/:path*",
}

LOOPBACK = ("localhost", "127.0.0.1")


def fail(msg):
    print("web-routes-check: FAIL - %s" % msg)
    raise SystemExit(1)


def main():
    ap = argparse.ArgumentParser(description="Assert built web route-manifest rewrites.")
    ap.add_argument("--manifest", default="web/.next/routes-manifest.json",
                    help="Path to routes-manifest.json (default: web/.next/routes-manifest.json)")
    ap.add_argument("--allow-missing", action="store_true",
                    help="Non-gating local mode: skip (exit 0) if the manifest is absent.")
    args = ap.parse_args()

    if not os.path.exists(args.manifest):
        if args.allow_missing:
            print("web-routes-check: SKIP (no manifest at %s)" % args.manifest)
            return
        fail("manifest absent at %s (build the web image first, or pass --allow-missing)" % args.manifest)

    try:
        with open(args.manifest) as fh:
            loaded = json.load(fh)
    except (ValueError, OSError) as e:
        fail("cannot parse manifest: %s" % e)
        return  # unreachable; keeps static analyzers satisfied

    if not isinstance(loaded, dict):
        fail("manifest is not a JSON object")
        return  # unreachable
    data = loaded

    rewrites = data.get("rewrites")
    if not isinstance(rewrites, dict):
        print("web-routes-check: FAIL - manifest missing 'rewrites' object")
        raise SystemExit(1)

    # Flatten all buckets into (source -> destination) pairs.
    seen = {}
    for bucket in ("beforeFiles", "afterFiles", "fallback"):
        entries = rewrites.get(bucket)
        if entries is None:
            continue
        if not isinstance(entries, list):
            fail("rewrites.%s is not a list" % bucket)
        for r in entries:
            src = r.get("source")
            dst = r.get("destination")
            if not src or not dst:
                fail("malformed rewrite entry in %s: %r" % (bucket, r))
            # Last matching source wins (Next semantics); keep the final one.
            seen[src] = dst

    # Reject any loopback destination outright.
    for src, dst in seen.items():
        low = dst.lower()
        if any(tok in low for tok in LOOPBACK):
            fail("loopback destination for %s -> %s" % (src, dst))

    # Require exact expected mappings.
    for src, want in EXPECTED.items():
        got = seen.get(src)
        if got is None:
            fail("required rewrite missing: %s" % src)
        if got != want:
            fail("rewrite mismatch for %s: got %s, want %s" % (src, got, want))

    print("web-routes-check: OK (all required rewrites exact, no loopback destinations)")


if __name__ == "__main__":
    main()
