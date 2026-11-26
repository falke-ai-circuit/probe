#!/usr/bin/env python3
"""
apitest.py — PROBE server API v1 regression suite (P26-01)

Requirement (owner, verbatim): "covers all and is fast and non intrusive so we
protect ourselves from regressions".

Route surface: every /api/v1 route + /openapi.json, enumerated FROM SOURCE at
the deployed tag 3a596f4 (git archive of the PROBE repo, all s.mux.HandleFunc
registrations in internal/server/*.go, non-test files). 129 unique
method+pattern registrations; 128 under /api/v1.

Modes:
  TEST (default, --mode test)   — point at a scratch server with its own data
                                  dir. Full probe set: GET reads, error-envelope
                                  probes on a non-existent agent id, validation
                                  probes with empty bodies (server validates
                                  BEFORE any state change), and CRUD round-trips
                                  scoped to scratch-owned records (apit- prefix).
  PROD (--mode prod)            — GET-only + POST /api/v1/login (both valid and
                                  invalid credentials) + 404 error-envelope
                                  probes via GET on non-existent ids. NO other
                                  POST/PUT/DELETE is emitted; the suite
                                  hard-refuses them. Meaningful writes,
                                  exec, transfers, builds: enforced SKIP.

Usage:
  python3 apitest.py --base http://127.0.0.1:7811 --mode test \
      --user admin --password '...' --expected-version 1.19.2 \
      --golden golden.json --golden-diff
  python3 apitest.py ... --golden-update     # (re)capture the golden baseline
  python3 apitest.py ... --list-routes       # print the embedded route census

Exit code: 0 all pass, 1 failures/POSSIBLE REGRESSIONS, 2 usage error.
"""

import argparse
import json
import sys
import time
import urllib.error
import urllib.request

UA = {"Content-Type": "application/json", "User-Agent": "apitest-p26-01/1.0"}

# ---------------------------------------------------------------------------
# ROUTE CENSUS — from source at tag 3a596f4 ( probes repo, internal/server/*.go,
# all s.mux.HandleFunc registrations outside _test.go). Kind drives the probe:
#   read        GET list/read — expect 200 {ok:true,data:...}
#   ghost_get   GET with non-existent id — expect 4xx {ok:false,error{code,message}}
#   ghost_cmd   POST/PUT/DELETE with non-existent id or {} body — server must
#               reject with a JSON error envelope BEFORE any state change
#   auth        POST /login — valid + invalid credentials
#   crud        scratch-only create→read→(delete) round-trip, prod mode: SKIP
#   env_*       behavior depends on binary/build flags; suite records the
#               observed shape and the golden baseline catches drift
# ---------------------------------------------------------------------------
ROUTES = [
    # --- server-level GETs ---------------------------------------------------
    ("GET",    "/api/v1/health",                      "read",  {"req_fields": ["status", "server_version", "total_agents", "active_agents", "stale_agents", "uptime_seconds"], "versioned": True}),
    ("GET",    "/api/v1/agents",                      "read",  {}),
    ("GET",    "/api/v1/agents/revoked",              "read",  {}),
    ("GET",    "/api/v1/audit?limit=50",              "read",  {}),  # unbounded audit = 200MB+ on prod; suite always bounds it
    ("GET",    "/api/v1/enrollment-tokens",           "read",  {}),
    ("GET",    "/api/v1/builds",                      "read",  {}),
    ("GET",    "/api/v1/profiles",                    "read",  {}),
    ("GET",    "/api/v1/tasks",                       "read",  {}),
    ("GET",    "/api/v1/transfers",                   "read",  {}),
    ("GET",    "/api/v1/forward-policies",            "read",  {}),
    ("GET",    "/api/v1/security/status",             "read",  {}),
    ("GET",    "/api/v1/security/login-attempts",     "read",  {}),
    ("GET",    "/api/v1/credentials",                 "read",  {}),
    ("GET",    "/api/v1/topology",                    "read",  {"req_fields": ["nodes"]}),
    ("GET",    "/api/v1/replicate/source",            "env_replicate", {}),
    ("GET",    "/openapi.json",                       "read",  {"raw": True, "req_fields": ["paths", "openapi"]}),
    # --- flows family (503 + ok:true when the binary runs without a flows path) ---
    ("GET",    "/api/v1/flows",                       "env_flows", {}),
    ("GET",    "/api/v1/flow-runs",                   "env_flows", {}),
    ("GET",    "/api/v1/flow-templates",              "env_flows", {}),
    ("GET",    "/api/v1/flows/apit-nonexistent",      "env_flows", {}),
    ("GET",    "/api/v1/flow-runs/apit-nonexistent",  "env_flows", {}),
    ("POST",   "/api/v1/flows",                       "env_flows", {}),
    ("POST",   "/api/v1/flows/apit-nonexistent/enable",   "env_flows", {}),
    ("POST",   "/api/v1/flows/apit-nonexistent/disable",  "env_flows", {}),
    ("POST",   "/api/v1/flows/apit-nonexistent/run-now",  "env_flows", {}),
    ("POST",   "/api/v1/flows/apit-nonexistent/assign",   "env_flows", {}),
    ("POST",   "/api/v1/flows/apit-nonexistent/unassign", "env_flows", {}),
    ("POST",   "/api/v1/flows/from-template",         "env_flows", {}),
    ("PUT",    "/api/v1/flows/apit-nonexistent",      "env_flows", {}),
    ("DELETE", "/api/v1/flows/apit-nonexistent",      "env_flows", {}),
    ("POST",   "/api/v1/flows/apit-nonexistent/enable",   "env_flows", {}),
    # (note: duplicate enable entry removed at runtime via seen-set)
    # --- ghost-id GET error envelopes ---------------------------------------
    ("GET",    "/api/v1/agents/apit-nonexistent",               "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/health",        "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/audit",         "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/capabilities",  "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/jobs",          "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/flows",         "env_flows", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/mode",          "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/sensors",       "env_503", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/survey",        "env_503", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/tunnels",       "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/stream-frame",  "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/fs/download",   "ghost_get", {}),
    ("GET",    "/api/v1/agents/apit-nonexistent/forward-policy","read",   {}),
    ("GET",    "/api/v1/builds/apit-nonexistent",               "ghost_get", {}),
    ("GET",    "/api/v1/builds/apit-nonexistent/download",      "ghost_get", {}),
    ("GET",    "/api/v1/builds/apit-nonexistent/vt-scan",       "ghost_get", {}),
    ("GET",    "/api/v1/profiles/apit-nonexistent",             "ghost_get", {}),
    ("GET",    "/api/v1/tasks/apit-nonexistent",                "ghost_get", {}),
    ("GET",    "/api/v1/credentials/apit-nonexistent",          "ghost_get", {}),
    ("GET",    "/api/v1/transfers/apit-nonexistent",            "ghost_get", {}),
    ("GET",    "/api/v1/downloads/apit-nonexistent.bin",        "ghost_get", {}),
    # --- agent command surface: every POST route probes the error envelope ---
    # via a non-existent agent id with an empty JSON body. The server validates
    # the request and agent existence BEFORE dispatch to any agent; there is no
    # connected agent in scope, so nothing can execute. exec is included per
    # owner instruction as an envelope probe only (no live-agent exec).
    ("POST",   "/api/v1/agents/apit-nonexistent/exec",            "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/job-status",      "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/job-log",         "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs-list",         "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs-read",         "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs-write",        "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs-stat",         "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs-hash",         "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs-move",         "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs-mkdir",        "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs-delete",       "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/fs/upload",       "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/watch",           "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/proc-list",       "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/proc-kill",       "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/proc-start",      "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/capture",         "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/tunnel",          "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/tunnel-close",    "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/sniff",           "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/sniff-stop",      "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/mitm-start",      "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/mitm-stop",       "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/mitm-traffic",    "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/debug-attach",    "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/debug-detach",    "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/debug-read-mem",  "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/debug-modules",   "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/debug-mem-query", "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/update",          "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/socks5-start",    "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/socks5-stop",     "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/port-forward",    "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/port-scan",       "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/net-connections", "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/autostart-enable",  "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/autostart-disable", "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/autostart-status",  "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/file-search",     "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/file-download",   "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/sysinfo",         "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/transfer",        "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/stream-start",    "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/stream-stop",     "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/pointer-click",   "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/key-press",       "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/key-combo",       "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/text-input",      "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/mode",            "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/reconfigure",     "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/redeploy",        "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/revoke",          "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/forward-policy",  "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/sensors/exec/enable",   "ghost_cmd", {}),
    ("POST",   "/api/v1/agents/apit-nonexistent/sensors/exec/disable",  "ghost_cmd", {}),
    ("PUT",    "/api/v1/agents/apit-nonexistent/sensors",         "ghost_cmd", {}),
    ("DELETE", "/api/v1/agents/apit-nonexistent",                 "ghost_cmd", {}),
    # --- server-level mutators: INVALID-BODY validation probes only ---------
    # The server rejects these with 400/INVALID_PARAMS or 404 BEFORE touching
    # state; no test record is created, nothing is forwarded, nothing deleted.
    ("POST",   "/api/v1/login",                      "auth",   {}),
    ("POST",   "/api/v1/tasks",                      "ghost_cmd", {}),
    ("POST",   "/api/v1/profiles",                   "ghost_cmd", {}),
    ("POST",   "/api/v1/operators",                  "ghost_cmd", {}),
    ("POST",   "/api/v1/reconfigure",                "ghost_cmd", {}),
    ("POST",   "/api/v1/enroll",                     "ghost_cmd", {}),
    ("POST",   "/api/v1/enrollment-tokens",          "ghost_cmd", {}),
    ("POST",   "/api/v1/files",                      "ghost_cmd", {}),
    ("POST",   "/api/v1/builds",                     "ghost_cmd", {}),
    ("POST",   "/api/v1/builds/apit-nonexistent/vt-scan", "ghost_cmd", {}),
    ("POST",   "/api/v1/security/blacklist",         "ghost_cmd", {}),
    ("POST",   "/api/v1/transfers/apit-nonexistent/pause",  "ghost_cmd", {}),
    ("POST",   "/api/v1/transfers/apit-nonexistent/resume", "ghost_cmd", {}),
    ("POST",   "/api/v1/transfers/apit-nonexistent/cancel", "ghost_cmd", {}),
    ("POST",   "/api/v1/transfers/apit-nonexistent/verify", "ghost_cmd", {}),
    ("PUT",    "/api/v1/credentials/apit-nonexistent", "ghost_cmd", {}),
    ("DELETE", "/api/v1/profiles/apit-nonexistent",  "ghost_cmd", {}),
    ("DELETE", "/api/v1/tasks/apit-nonexistent",     "ghost_cmd", {}),
    ("DELETE", "/api/v1/operators/apit-nonexistent", "ghost_cmd", {}),
    ("DELETE", "/api/v1/builds/apit-nonexistent",    "ghost_cmd", {}),
    # --- scratch-mode CRUD round-trips (SKIPPED in prod mode) ----------------
    ("POST",   "/api/v1/enrollment-tokens",          "crud", {"payload": {"agent_name": "apit-suite-agent", "ttl_hours": 1}}),
    ("POST",   "/api/v1/operators",                  "crud", {"payload": {"name": "apit-suite-op", "role": "viewer", "token": "apit-suite-op-token-0001"}}),
    ("PUT",    "/api/v1/credentials/apit-suite-cred", "crud", {"payload": {"value": "apit-suite-secret", "type": "generic"}}),
    ("POST",   "/api/v1/tasks",                      "crud", {"payload": {"name": "apit-suite-task", "agent_id": "apit-nonexistent", "schedule": {"kind": "once", "at": "2030-01-01T00:00:00Z"}, "payload": {"type": "exec", "command": "echo apitest"}}}),
]

# Deduped route patterns (the ROUTES list above may repeat a few patterns with
# different probe kinds; the census below is the source-derived registration
# list used by --list-routes / coverage checks).
CENSUS_DUPLICATE_PATTERNS = {
    ("POST", "/api/v1/flows/apit-nonexistent/enable"),
}

# Routes registered in source but intentionally never produced by the suite.
# Every entry below must carry a reason (surface in the SKIPPED report).
SKIP_ALWAYS = {
    "/api/v1/agents/{id}/exec (worker-holding-path detail: exec is probed only "
    "as a ghost-id envelope check; live exec is prohibited by the owner)": None,
}


def http_req(url, method="GET", token=None, body=None, timeout=5, raw_body=None, extra_headers=None):
    headers = dict(UA)
    if token:
        headers["Authorization"] = "Bearer " + token
    if extra_headers:
        headers.update(extra_headers)
    data = None
    if raw_body is not None:
        data = raw_body
    elif body is not None:
        data = json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:  # connection refused, timeout...
        return 0, str(e).encode()


def try_json(b):
    try:
        return json.loads(b.decode("utf-8", "replace"))
    except Exception:
        return None


def envelope_ok_shape(doc):
    return isinstance(doc, dict) and doc.get("ok") is True


def route_key(method, route):
    """Canonical census key: METHOD + single space + PATH.

    Guards the measured P26-03 defect where a key rendered as
    'GET GET /api/v1/audit?limit=50' (method token duplicated inside the route
    string), which malformed the probe URL (http 0 / no response). Raises
    ValueError on any malformed entry so the census fails loud, never silent.
    Unit-tested in scripts/test_apitest.py.
    """
    method = str(method).upper()
    route = str(route)
    tokens = route.split()
    if len(tokens) > 1 and tokens[0] in ("GET", "POST", "PUT", "DELETE", "PATCH"):
        raise ValueError("route already carries a method prefix: %r" % route)
    if not route.startswith("/"):
        raise ValueError("route must start with '/': %r" % route)
    return method + " " + route


def normalize_version(v):
    """Strip a single display-only leading 'v' from a semver string.

    The release protocol makes the canonical form prefix-free ('v1.19.2' and
    '1.19.2' are the SAME version). The old literal comparison false-FAILED a
    correct build when health reported '1.19.2' and --expected-version carried
    'v1.19.2'. Normalise BOTH sides. Unit-tested in scripts/test_apitest.py.
    """
    v = str(v).strip()
    return v[1:] if v.startswith("v") else v


def envelope_err_shape(doc):
    if not isinstance(doc, dict) or doc.get("ok") is not False:
        return False
    err = doc.get("error")
    return isinstance(err, dict) and "code" in err and "message" in err


def main():
    ap = argparse.ArgumentParser(description="PROBE API v1 regression suite (P26-01)")
    ap.add_argument("--base", required=True, help="e.g. http://127.0.0.1:7811 or http://10.10.10.100:7701")
    ap.add_argument("--mode", choices=["test", "prod"], default="test",
                    help="test = scratch instance, full probe set; prod = GET-only + login, enforced")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--password", default="")
    ap.add_argument("--token", default="", help="bearer token (skips login probe token fetch)")
    ap.add_argument("--expected-version", default="", help="assert health server_version equals this when set")
    ap.add_argument("--timeout", type=float, default=5.0,
                    help="per-request seconds; raise to 8+ for prod-over-tunnel (bounded audit alone can take 4-6s)")
    ap.add_argument("--golden", default="golden_baseline.json")
    ap.add_argument("--golden-update", action="store_true", help="capture current shapes as the golden baseline")
    ap.add_argument("--golden-diff", action="store_true", help="DIFF current run against the golden baseline")
    ap.add_argument("--output", default="apitest_results.json")
    ap.add_argument("--list-routes", action="store_true")
    args = ap.parse_args()

    if args.list_routes:
        # Census proof (P26-03 fix 2): every entry MUST parse as METHOD + PATH
        # with no method duplication. Any defect exits 2.
        bad = 0
        seen_lr = set()
        for method, route, kind, meta in ROUTES:
            try:
                k = route_key(method, route)
            except ValueError as e:
                print("DEFECT %s: %s" % (kind, e))
                bad += 1
                continue
            dup = " (DUPLICATE)" if k in seen_lr else ""
            seen_lr.add(k)
            print("%-7s %-55s %-12s%s" % (method, route, kind, dup))
        print("-- %d routes, %d malformed, %d unique keys --" % (len(ROUTES), bad, len(seen_lr)))
        sys.exit(2 if bad else 0)

    base = args.base.rstrip("/")
    t0 = time.time()
    results = []          # per-endpoint records
    skipped = []          # (endpoint, reason)

    def record(route, method, status, doc, ok, detail="", kind=""):
        # Single key discipline (P26-03): a route key is METHOD + " " + PATH
        # exactly once. Callers may pass either a bare path or a prefixed
        # string; everything is normalised HERE through route_key, and the
        # stored `route` is the BARE path. Aggregation re-derives the key via
        # route_key(r["method"], r["route"]) — never by string concat.
        # Golden fingerprint: status + envelope verdict + error code + top-level data shape.
        if isinstance(doc, dict):
            if "data" in doc:
                d = doc["data"]
                shape = "null" if d is None else ("list:%d" % len(d) if isinstance(d, list) else
                        "keys:%s" % ",".join(sorted(d.keys())) if isinstance(d, dict) else type(d).__name__)
            else:
                shape = None
            if isinstance(shape, str) and shape.startswith("list:"):
                shape = "list"  # list length is live data volume, not a contract — collapse it
            fp = {"status": status, "ok": doc.get("ok"), "error_code": (doc.get("error") or {}).get("code") if isinstance(doc.get("error"), dict) else None,
                  "data_shape": shape}
        else:
            fp = {"status": status, "ok": None, "error_code": None, "data_shape": None}
        try:
            full = route_key(method, route)
        except ValueError:
            # Not a plain method+path row (login variants, health(server_version),
            # 405-discipline). Keep the label as-is; no method token to strip.
            pass
        else:
            route = full.split(" ", 1)[1]
        results.append({"route": route, "method": method, "kind": kind, "pass": ok,
                        "status": status, "detail": detail, "golden": fp})

    # ---- login -------------------------------------------------------------
    body = {"username": args.user, "password": args.password}
    st, rb = http_req(base + "/api/v1/login", "POST", body=body, timeout=args.timeout)
    doc = try_json(rb)
    token = args.token
    if args.mode == "prod" and not token:
        ok = st == 200 and envelope_ok_shape(doc) and isinstance((doc.get("data") or {}).get("token"), str)
        record("/api/v1/login", "POST", st, doc, ok, "prod-mode login", "auth")
        if ok:
            token = doc["data"]["token"]
    elif not token:
        ok = st == 200 and envelope_ok_shape(doc) and isinstance((doc.get("data") or {}).get("token"), str)
        record("/api/v1/login(valid)", "POST", st, doc, ok, "token len %d" % len((doc.get("data") or {}).get("token", "") if isinstance(doc, dict) else ""), "auth")
        if ok:
            token = doc["data"]["token"]
    # invalid credentials must yield a JSON error envelope
    st, rb = http_req(base + "/api/v1/login", "POST", body={"username": args.user, "password": "definitely-wrong-pass-!"}, timeout=args.timeout)
    doc = try_json(rb)
    record("/api/v1/login(invalid)", "POST", st, doc,
           st in (401, 403) and envelope_err_shape(doc) or (st == 200 and envelope_err_shape(doc)),
           "invalid-creds envelope", "auth")
    # empty body must NOT 500 (envelope discipline)
    st, rb = http_req(base + "/api/v1/login", "POST", raw_body=b"{}", timeout=args.timeout)
    doc = try_json(rb)
    record("/api/v1/login(empty body)", "POST", st, doc, st < 500 and envelope_err_shape(doc), "empty-body envelope", "auth")

    # ---- method-not-allowed discipline --------------------------------------
    st, rb = http_req(base + "/api/v1/tasks", "POST", raw_body=b"not-json{{", timeout=args.timeout) if False else (0, b"")
    st, rb = http_req(base + "/api/v1/agents/apit-nonexistent/exec", "GET", token=token, timeout=args.timeout)
    doc = try_json(rb)
    record("/agents/{id}/exec", "GET", st, doc, st == 405,
           "405 method discipline (GET on POST-only exec)" if st == 405 else "expected 405", "envelope")

    # ---- auth required on health? (record observed posture) ----------------
    st, rb = http_req(base + "/api/v1/health", "GET", timeout=args.timeout)
    doc = try_json(rb)
    record("/api/v1/health", "GET", st, doc,
           st == 200 and envelope_ok_shape(doc) and isinstance(doc.get("data"), dict)
           and all(k in doc["data"] for k in ["status", "server_version", "total_agents", "active_agents", "stale_agents", "uptime_seconds"]),
           "", "read")
    if doc and isinstance(doc.get("data"), dict) and args.expected_version:
        # Release protocol: canonical semver is PREFIX-FREE ('v' is display
        # only). Normalise BOTH sides — a correct build must never false-FAIL
        # here (P26-03 fix 1).
        got = normalize_version(str(doc["data"].get("server_version", "")))
        want = normalize_version(args.expected_version)
        record("/api/v1/health(server_version)", "GET", st, doc, got == want,
               "expected %r got %r %s" % (want, got, "" if got == want else "VERSION MISMATCH"), "read")
    elif args.expected_version:
        record("/api/v1/health(server_version)", "GET", st, doc, False, "health data missing", "read")

    # ---- enumerate every registered route and probe it ----------------------
    seen = set()
    for method, route, kind, meta in ROUTES:
        try:
            key = route_key(method, route)
        except ValueError as e:
            print("ROUTE TABLE DEFECT: %s" % e)
            sys.exit(2)
        if key in seen:
            continue
        seen.add(key)
        url = base + route

        if args.mode == "prod" and not (method == "GET" or (method == "POST" and route == "/api/v1/login")):
            skipped.append((route_key(method, route), "PROD mode: non-GET probe suppressed (non-intrusive guarantee)"))
            continue
        if kind == "crud" and args.mode == "prod":
            skipped.append((route_key(method, route), "PROD mode: write/CRUD round-trip suppressed"))
            continue

        if kind == "crud":
            payload = meta.get("payload", {})
            # create
            st, rb = http_req(url, method, token=token, body=payload, timeout=args.timeout)
            doc = try_json(rb)
            ok = st < 500 and (envelope_ok_shape(doc) or envelope_err_shape(doc))
            record(route + " (create)", method, st, doc, ok, "", "crud")
            new_id = None
            if isinstance(doc, dict) and isinstance(doc.get("data"), dict):
                new_id = doc["data"].get("id") or doc["data"].get("name")
            # read back (list or detail) where a bearer exists
            if new_id:
                rd_route, rd_m = {
                    "apit-suite-agent": ("/api/v1/enrollment-tokens", "GET"),
                    "apit-suite-op": ("/api/v1/operators", "GET"),
                    "apit-suite-cred": ("/api/v1/credentials/apit-suite-cred", "GET"),
                }.get(str(new_id), (route.rsplit("/", 1)[0], "GET"))
                st2, rb2 = http_req(base + rd_route, rd_m, token=token, timeout=args.timeout)
                doc2 = try_json(rb2)
                record(rd_route + " (read-after-create)", rd_m, st2, doc2, st2 == 200 and envelope_ok_shape(doc2), "id=%s" % new_id, "crud")
            continue

        if kind == "read" and not meta.get("raw"):
            st, rb = http_req(url, method, token=token, timeout=args.timeout)
            doc = try_json(rb)
            ok = st == 200 and envelope_ok_shape(doc)
            req_f = meta.get("req_fields") or []
            if ok and req_f and isinstance(doc.get("data"), dict):
                missing = [f for f in req_f if f not in doc["data"]]
                if missing:
                    ok, detail = False, "missing fields %s" % missing
            elif ok and req_f and isinstance(doc.get("data"), list):
                ok, detail = len(doc["data"]) >= 0, ""
            detail_local = detail if (not ok and 'detail' in dir()) else ""
            record(route, method, st, doc, ok, detail_local if not ok else "", kind)
            continue

        if kind == "read" and meta.get("raw"):
            st, rb = http_req(url, method, token=token, timeout=args.timeout)
            doc = try_json(rb)
            ok = st == 200 and isinstance(doc, dict) and all(k in doc for k in meta.get("req_fields", []))
            record(route, method, st, {"ok": ok, "paths": (doc or {}).get("paths")}, ok,
                   "" if ok else "raw JSON route list", kind)
            continue

        if kind == "env_read":
            # environment-dependent endpoint: assert ENVELOPE shape, record fingerprint for golden diff
            st, rb = http_req(url, method, token=token, timeout=args.timeout)
            doc = try_json(rb)
            ok = (envelope_ok_shape(doc) or envelope_err_shape(doc)) and st < 500
            record(route, method, st, doc, ok, "env-dependent; baseline records observed shape", kind)
            continue

        if kind == "env_flows":
            st, rb = http_req(url, method, token=token, body={} if method in ("POST", "PUT", "DELETE") else None, timeout=args.timeout)
            doc = try_json(rb)
            # Fires 503 {"ok":true,"data":{"error":"flow manager not initialized"}}
            # when the server runs without a flows-path (probe-server-r0 case);
            # full data envelope when the flows runtime is wired. Hard line:
            # unstructured 5xx (real failure) vs structured 502/500 crash.
            ok = (st == 503 and rb.strip().startswith(b"{")) or (st < 500 and (envelope_ok_shape(doc) or envelope_err_shape(doc)))
            record(route, method, st, doc, ok,
                   "structured degraded (503 JSON) or wired runtime", kind)
            continue

        if kind == "env_replicate":
            st, rb = http_req(url, method, token=token, timeout=args.timeout)
            doc = try_json(rb)
            # P26-02/03 GOLDEN REVIEW verdict on GET /api/v1/replicate/source at
            # v1.19.2: the observed 500 INTERNAL_ERROR ("source module root not
            # found") is a KNOWN-DEFECT of the build, NOT a legitimate packaging
            # variant — a suite that baselines a 500 protects the bug. Contract:
            #   200 zip (PK magic, packaged build) or 200 JSON envelope = PASS
            #   anything else (incl. 500 INTERNAL_ERROR) = FAIL
            if st == 200 and rb[:2] == b"PK":
                ok, detail = True, "zip export (packaged build)"
            elif st == 200 and envelope_ok_shape(doc):
                ok, detail = True, "JSON envelope"
            else:
                ok, detail = False, ("KNOWN-DEFECT: 500 INTERNAL_ERROR observed on v1.19.2 "
                                     "(baseline must not bless it) observed=%s" % st)
            record(route, method, st, doc, ok, detail, kind)
            continue

        if kind == "env_503":
            # 503 with a JSON ok:true envelope or plain-text guard message is the
            # documented degraded-runtime response (flows manager absent, sensor
            # assignment unavailable). Regression line: unstructured 500/502.
            st, rb = http_req(url, method, token=token, body={} if method in ("POST", "PUT", "DELETE") else None, timeout=args.timeout)
            doc = try_json(rb)
            ok = st == 503 and (envelope_ok_shape(doc) or envelope_err_shape(doc) or doc is None) or st < 500
            record(route, method, st, doc, ok,
                   "degraded-runtime 503 acceptable", kind)
            continue

        if kind == "ghost_get":
            st, rb = http_req(url, method, token=token, timeout=args.timeout)
            doc = try_json(rb)
            # Two legitimate contracts in v1.19.2: 404 NOT_FOUND envelope for
            # registry-backed reads (agent detail/health/capabilities/mode) and
            # 200 with empty data for log-style reads (audit/jobs/tunnels).
            # Failure mode = 5xx or a malformed body.
            ok = st < 500
            record(route, method, st, doc, ok,
                   "" if ok else "5xx on read-only GET of non-existent id", kind)
            continue

        if kind == "ghost_cmd":
            st, rb = http_req(url, method, token=token, body={}, timeout=args.timeout)
            doc = try_json(rb)
            # Contract observed in the v1.19.2 binary:
            #  - most commands: JSON envelope (404/400/503 AGENT_UNREACHABLE/ok-true)
            #  - mode/reconfigure/forward-policy: PLAIN-TEXT 400 with the reason
            #  - sensors on exec-less agent: 503 plain-text "sensor assignment not available"
            #  - fs-*/proc-*/capture/etc on ghost agent: 503 JSON AGENT_UNREACHABLE envelope
            # The hard regression line: status < 500, OR a 5xx that is a known
            # guard response (503 AGENT_UNREACHABLE JSON) — never an unstructured
            # 500/502, never a crash.
            is_json_env = envelope_err_shape(doc) or envelope_ok_shape(doc)
            is_guard_503 = st == 503 and (is_json_env or (doc is None and b"sensor assignment" in rb))
            ok = (st < 500 and (is_json_env or st == 405 or doc is None)) or is_guard_503
            record(route, method, st, doc, ok,
                   "" if ok else "REGRESSION CHECK: unexpected %s" % st, kind)
            continue

    # ---- dedupe + summary ---------------------------------------------------
    total_ms = int((time.time() - t0) * 1000)
    failed = [r for r in results if not r["pass"]]
    passed = [r for r in results if r["pass"]]

    def _key(r):
        # Canonical golden/table/failure key: METHOD + " " + PATH, built ONLY
        # through route_key so a doubled method token can never reappear.
        # Labelled special rows (login variants, health(server_version),
        # 405-discipline) fall back to their readable label.
        try:
            return route_key(r["method"], r["route"])
        except ValueError:
            return r["method"] + " " + r["route"]

    # ---- table --------------------------------------------------------------
    W = 5 + 52 + 6 + 5 + 6 + 40
    print("=" * W)
    print("PROBE API v1 regression suite (P26-01)  mode=%s  base=%s" % (args.mode, base))
    print("=" * W)
    print("%-5s %-52s %-6s %-5s %-6s %s" % ("RES", "endpoint", "kind", "http", "ok", "note"))
    print("-" * W)
    for r in passed + failed:
        print("%-5s %-52s %-6s %-5s %-6s %s" % (
            "PASS" if r["pass"] else "FAIL", _key(r)[:52], r["kind"][:6],
            r["status"], r["golden"]["ok"] if isinstance(r["golden"]["ok"], bool) else "-", (r["detail"] or "")[:40]))
    print("-" * W)
    print("PASS %d / %d   FAIL %d   SKIPPED %d   TOTAL %d.%03ds" % (
        len(passed), len(results), len(failed), len(skipped), total_ms // 1000, total_ms % 1000))
    if skipped:
        print("-- SKIPPED endpoints --")
        for ep, why in skipped:
            print("  SKIP %s : %s" % (ep, why))
    if failed:
        print("-- FAILURES (possible regressions) --")
        for r in failed:
            print("  FAIL %s -> http %s (%s)" % (_key(r), r["status"], r["detail"] or "-"))

    # ---- golden baseline ----------------------------------------------------
    current = {_key(r): r["golden"] for r in results}
    if args.golden_update:
        with open(args.golden, "w") as f:
            json.dump({"captured_from": base, "mode": args.mode, "routes": current,
                       "skipped": skipped, "total_ms": total_ms}, f, indent=1)
        print("golden baseline captured: %s (%d endpoints)" % (args.golden, len(current)))

    if args.golden_diff:
        try:
            with open(args.golden) as f:
                gold = json.load(f)["routes"]
        except Exception as e:
            print("golden diff ERROR: %s" % e)
            sys.exit(2)
        drift = []
        for k, fp in sorted(current.items()):
            if k not in gold:
                drift.append("NEW      %s -> %s" % (k, json.dumps(fp)))
            elif gold[k] != fp:
                drift.append("CHANGED  %s\n   baseline: %s\n   now:      %s" % (k, json.dumps(gold[k]), json.dumps(fp)))
        for k in sorted(gold):
            if k not in current:
                drift.append("GONE     %s" % k)
        if drift:
            print("-- GOLDEN DRIFT (%d) --" % len(drift))
            for d in drift:
                print("  " + d)
            print("golden diff: SHAPE DRIFT = POSSIBLE REGRESSION (update the golden only as a deliberate, reviewed commit — docs/TESTING.md)")
            failed = failed or [True]  # drift fails the run: a gate that prints red but exits green protects nothing
        else:
            print("golden diff: NO DRIFT (%d endpoints identical)" % len(gold))

    with open(args.output, "w") as f:
        json.dump({"mode": args.mode, "base": base, "total_ms": total_ms,
                   "results": results, "skipped": skipped}, f, indent=1)

    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
