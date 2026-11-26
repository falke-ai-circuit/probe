# API Regression Suite (apitest.py) — P26-01/P26-03 standard

`scripts/apitest.py` is the API-shape regression gate for the PROBE server.
It gets its route census FROM SOURCE (`scripts/apitest.py` ROUTES table,
enumerated from every `s.mux.HandleFunc` registration in `internal/server/*.go`
at tag 3a596f4; 129 unique method+pattern registrations, 128 under `/api/v1`)
and asserts, per endpoint: status code, response envelope shape
(`{ok:true,data}` or `{ok:false,error:{code,message}}`), required fields, and
`--golden-diff` fingerprint drift against the committed baseline.

## Invocations

```bash
# full suite against a scratch server (default mode = test; writes only here)
python3 scripts/apitest.py \
  --base http://127.0.0.1:7811 --mode test \
  --user admin --password '...' \
  --golden scripts/golden/golden_baseline.json --golden-diff

# print the embedded route census
python3 scripts/apitest.py --list-routes

# unit tests for the suite helpers
python3 scripts/test_apitest.py
```

Exit codes: 0 all pass, 1 failures/possible regressions, 2 usage error.

## Budget: <= 120 s (hard)

- measured: test mode 134 probes in ~3.4 s; prod mode 48 probes in ~12.4 s.
- CI enforces the budget: `total_ms > 120000` fails the job even if no
  endpoint failed.

## Non-intrusive rule (hard)

- `--mode prod` emits GETs and `POST /api/v1/login` (valid + invalid) ONLY.
  Every non-GET probe is hard-refused and reported per-endpoint with a reason
  (86 endpoints skipped on the v1.19.2 surface).
- Writes, exec, transfers, builds, CRUD round-trips run ONLY in `--mode test`
  against a scratch instance with its OWN data dir (datadir/, PROBE_* paths,
  PROBE_SECRET_STORE). Never point test mode at prod.
- Prod runs go through a tunnel and are GET-only; zero restarts, zero deletes,
  zero container changes.

## Golden baselines

Repository copies are the standard of record:

- `scripts/golden/golden_baseline.json` — test instance, v1.19.2, 133 endpoint
  fingerprints.
- `scripts/golden/prod_golden_1.19.2.json` — prod GET-only reference shapes,
  OVH v1.19.2, 48 fingerprints (+86 documented skips).

A fingerprint is `{status, ok, error_code, data_shape}` with list lengths
collapsed to `list` so live data volume never false-positives.

### HOW TO UPDATE THE GOLDEN FILE — deliberate, never automatic

`--golden-update` OVERWRITES the baseline with whatever the current build
returns. It must ALWAYS be a conscious, reviewed commit:

1. Run the suite with `--golden-update` against the target instance.
2. `git diff scripts/golden/` — read every changed fingerprint line by line.
3. Justify each change in the commit message (endpoint / old -> new / why the
   new shape is the Correct Contract and not a regression you just blessed).
4. Commit. Never chain `--golden-update` into CI, deploy scripts, or release
   automation. A golden that updates itself protects nothing.

## GOLDEN REVIEW — 5xx / INTERNAL_ERROR census (P26-03)

The originally captured test baseline contained a measured defect:
`GET /api/v1/replicate/source` recorded `500 INTERNAL_ERROR, pass:true` — the
golden was CAPTURED from the current build, so it blessed an internal error.
Every 5xx entry in the baseline was reviewed:

| Entry (group) | Count | Decision | Reason |
|---|---|---|---|
| `GET /api/v1/replicate/source` (`500 INTERNAL_ERROR`) | 1 | **FIXED-BASELINE + KNOWN-DEFECT marker** | Measured to be a build/deployment defect (source module root not found), not a legitimate packaging variant: a server run from a source checkout returns `200` with a PK-magic zip (`source_export.go` walks up from the module root). The committed golden now records the `200 <zip>` fingerprint, and `env_replicate` assertion was hardened: ANY non-200 (incl. `500 INTERNAL_ERROR`) is reported `KNOWN-DEFECT ... observed=` and FAILS the suite. A suite that baselines a 500 protects the bug. |
| flows family + flows/sensors degraded reads (`503 {ok:true,data:{error:...}}`) | 18 | PASS-as-contract (documented degraded-runtime shape) | The flows manager is not initialized in builds without a flows path; `503` + structured JSON envelope is the DOCUMENTED contract (`env_flows` rule). Regression line enforced separately: unstructured 5xx / structured 502/500 crash FAILs. Not marked KNOWN-DEFECT because it is not a defect. |
| ghost-agent command probes (`503 AGENT_UNREACHABLE / SERVICE_UNAVAILABLE / AGENT_UNAVAILABLE`) | 20 + 17 + 1 | PASS-as-contract (documented guard envelopes) | Commands against a non-existent agent id with `{}` body: the v1.19.2 binary rejects with structured JSON guard envelopes before any state change (`ghost_cmd` rule). Regression line enforced separately: unexpected 5xx or malformed body FAILs. Not marked KNOWN-DEFECT because it is not a defect. |

Net result after review: the committed `golden_baseline.json` contains ZERO
`500`/`INTERNAL_ERROR` fingerprints. Remaining 5xx entries are all `503`
structured-contract fingerprints asserted by explicit kind rules; none are
blessed `pass:true` internal errors.

## CI

Job: **`api-shape-gate`** in `.github/workflows/apitest.yml`.

- Triggers: push to `main` and `release/**`, plus `workflow_dispatch`
  (with a `drift` input used for the deliberate-drift self-test that proves
  the gate can go RED).
- Builds the server from the checked-out tree, boots a scratch instance with
  its own datadir/secret store, runs the full test-mode suite with
  `--golden-diff` against the committed standard, enforces the 120 s budget,
  and FAILS the build on any endpoint failure or fingerprint drift.
- Products (`results jsons`) are written to a temp dir and never committed.
