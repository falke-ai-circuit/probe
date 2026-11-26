#!/usr/bin/env python3
"""Unit tests for scripts/apitest.py helpers (P26-03).

Why these exist (both bugs were MEASURED on live runs, not theorised):

  1. VERSION NORMALISATION — on v1.19.2 the health endpoint reports
     '1.19.2' (canonical, prefix-free per the release protocol) while the
     suite was invoked with --expected-version 'v1.19.2'. The old literal
     comparison produced a false FAIL on a CORRECT build. normalize_version()
     strips a display-only leading 'v' from BOTH sides.

  2. ROUTE TABLE KEY — one census entry rendered its golden key as
     'GET GET /api/v1/audit?limit=50' (method token duplicated inside the
     route string), which malformed the probe URL and produced http 0.
     route_key() is the single key builder; it must parse every census entry
     as METHOD + single-space + PATH and RAISE on any entry that already
     carries a method prefix.

Run:  python3 scripts/test_apitest.py
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import apitest  # noqa: E402


def test_normalize_version_forms():
    cases = [
        ("1.19.2", "1.19.2"),      # canonical, unchanged
        ("v1.19.2", "1.19.2"),     # display-v stripped
        ("v1.19.2-rc1", "1.19.2-rc1"),  # prerelease tail preserved
        ("v2.0.0", "2.0.0"),
        ("", ""),                  # empty stays empty (never crashes)
        ("v", ""),                 # lone v degrades to empty, no IndexError
    ]
    for got, want in cases:
        out = apitest.normalize_version(got)
        assert out == want, "normalize_version(%r) = %r, want %r" % (got, out, want)


def test_version_comparison_symmetric():
    # BOTH spellings of the same version must compare equal — the exact
    # false-FAIL from the live run ('expected v1.19.2 got 1.19.2').
    a = apitest.normalize_version("v1.19.2")
    b = apitest.normalize_version("1.19.2")
    assert a == b, "v-prefixed and plain %r/%r must be equal" % (a, b)
    # and an actual mismatch must still be caught
    assert apitest.normalize_version("v1.19.3") != apitest.normalize_version("1.19.2")


def test_route_key_shape():
    assert apitest.route_key("GET", "/api/v1/audit?limit=50") == "GET /api/v1/audit?limit=50"
    assert apitest.route_key("post", "/api/v1/login") == "POST /api/v1/login"
    assert apitest.route_key("DELETE", "/api/v1/builds/x") == "DELETE /api/v1/builds/x"


def test_route_key_rejects_method_prefixed_route():
    # The measured defect: 'GET GET /api/v1/audit?limit=50' can NEVER be
    # produced again — a method-bearing route string must raise, loudly.
    for bad in ("GET /api/v1/audit", "POST /api/v1/login", "GET GET /api/v1/audit?limit=50"):
        try:
            apitest.route_key("GET", bad)
        except ValueError:
            pass
        else:
            raise AssertionError("route_key('GET', %r) must raise ValueError" % bad)


def test_route_key_rejects_non_path():
    for bad in ("", "api/v1/x"):
        try:
            apitest.route_key("GET", bad)
        except ValueError:
            pass
        else:
            raise AssertionError("route_key('GET', %r) must raise ValueError" % bad)


def test_full_census_parses():
    # --list-routes contract, as a test: EVERY embedded census entry must parse
    # to a unique METHOD + PATH key with no duplication and no defects.
    seen = set()
    for method, route, kind, meta in apitest.ROUTES:
        key = apitest.route_key(method, route)  # raises on any malformed entry
        method_token, _, path = key.partition(" ")
        assert method_token in ("GET", "POST", "PUT", "DELETE", "PATCH"), key
        assert path.startswith("/"), key
        assert " " not in path or "?limit" in path, "space inside path: %r" % key  # audit bound stays one token
        seen.add(key)
    assert len(seen) >= 128, "census must stay >= 128 unique /api/v1 keys, got %d" % len(seen)


def test_census_audit_entry_not_double_method():
    # Regression pin for the exact measured key.
    keys = {apitest.route_key(m, r) for m, r, _, _ in apitest.ROUTES}
    assert "GET /api/v1/audit?limit=50" in keys
    assert not any(k.split(" ")[1].startswith("GET ") or k.split(" ")[1].startswith("POST ") for k in keys), \
        "some census key still carries a doubled method token"


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    for fn in fns:
        fn()
        print("PASS %s" % fn.__name__)
    print("ALL %d TESTS PASS" % len(fns))
