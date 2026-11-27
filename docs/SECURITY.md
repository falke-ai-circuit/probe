# PROBE security notes — P19 spec-text errata

This file records the security spec text owed by closed P19 units (P19-02
design, P19-03 falsify, P19-04 rotation, P19-05R falsify, P19-06 remediation).
The repo previously carried none of it: the per-agent E2E key spec lived only
in the closed artefact /opt/data/tmp/probe1/P19-02_PERAGENT_KEYS.md. Unit
P19-07 transcribes the four owed items here; the closed artefacts under
/opt/data/tmp stay as historical record and are not edited.

## 1. Per-agent E2E keys — posture and mid-session flag flips (P19-02 §4 F-4, P19-03 B2)

The per-agent E2E key design (HKDF over static-static ECDH of the agent's
enrollment key with the server CA key; the shared token is an input nowhere)
is specified in P19-02_PERAGENT_KEYS.md (closed design artefact). Posture set
at registration is session-fenced: key material is derived once at
registration from the cert presented then, never re-looked-up per frame.

The same fence applies to the agent-side downgrade posture. Wherever the
opt-out flag `allow_shared_token_e2e` appears in the compat matrix (P19-02
§5 row M-4: default REFUSE on shared-token downgrade; the opt-out flips
refuse → continue-with-persistent-loud-log), the spec text carries this
sentence:

> flag flips (allow_shared_token_e2e) are not retroactive — a flipped posture
> takes effect at next registration; the live session continues under its
> registered posture.

This names the one gap P19-03 B2 found: the matrix pinned posture per
(re)registration and never re-evaluated mid-session. The state is loud by
the same fencing that makes cert revocation not cut a live socket (F-4's
named, accepted cost).

## 2. Rollout R4 strict mode — the "agent opted out + server strict" refusal path (P19-02 §6 R4, P19-03 B4)

P19-03 B4's residual: R4's `--e2e-require-peragent` strict flag and the
agent-side `allow_shared_token_e2e` opt-out interact, and the spec named
neither side's typed refusal code for the combined state "agent opted out of
per-agent enforcement AND server strict". Named explicitly here, using the
codes the spec text defines:

- Server strict mode refuses shared-token/plaintext agent traffic with the
  typed refusal `E2E_KEY_REQUIRED` (P19-02 §6 R4; §5 row M-5). In the
  combined state the server is the refusing side: the connection never
  upgrades, so no shared-token frame flows at all.
- The agent's opted-out "continue-with-persistent-loud-log" posture governs
  only its OWN downgrade detection (P19-02 §5 row M-4: an OLD server, or a
  new server with e2e off, that ignores the agent's E2E offer). That agent
  side refusal path — disconnect with downgrade notice, reconnect loop
  reports `E2E_SHARED_TOKEN_DOWNGRADE`, status surface carries the refusal
  code — does not fire against a strict server, because the strict server's
  `E2E_KEY_REQUIRED` refusal answers the upgrade before any inbound frame
  exists to downgrade.
- The combined state is therefore both-party-loud (P19-03 B4's conclusion,
  preserved verbatim in intent): the server emits `E2E_KEY_REQUIRED`, and
  the agent's persistent loud log records its opted-out posture. Neither
  side is silent; R1-before-R3 remains operator discipline, not an
  invariant.
- Honesty note on "the code that exists in the tree": at the recorded HEAD
  (2f89f7c) no `allow_shared_token_e2e` flag or `--e2e-require-peragent`
  flag exists in code — P19-02 is a design-only artefact and this milestone
  has not been implemented. The typed codes above live in the spec text;
  the only wired-up agent-side typed refusal in this family at HEAD is the
  P19-04 5x401 bound (internal/agent/agent.go, runOutbound), quoted in §3.
  When R3/R4 land, the M-4 downgrade path must emit
  `E2E_SHARED_TOKEN_DOWNGRADE` and the strict server must emit
  `E2E_KEY_REQUIRED` exactly as named here.

## 3. Token rotation — restart resilience supersedes the P19-04 grace text (P19-06)

P19-04 §2's "orphan life bounded at one interval" and §7's "agents reconnect
with old or rotated tokens by design" — both were false across a server
restart (P19-05R F1). Corrected by P19-06 (ce82ff1): rotatedTokens now
persists via `--rotated-tokens-db` and reloads before connections are
accepted; the restart-orphan is closed; claim reads "grace survives restart
after P19-06" (deployed servers should point `--rotated-tokens-db` at a
persistent volume, matching the sibling *-db defaults).

Mechanics (P19-06, commit ce82ff1): the rotated-token store reuses the repo's
restart-surviving pattern (enrollment.json load-at-boot + writeFileAtomic
temp+rename+fsync); path resolution mirrors `--enrollment-db` exactly — flag
`--rotated-tokens-db` → env `PROBE_ROTATED_TOKENS_DB` → default
`/tmp/probe-rotated-tokens.json` in both `cmd/probe/serve.go` and
`cmd/probe-server/main.go`. Operators on the pinned image add
`- --rotated-tokens-db /data/runtime/rotated-tokens.json` at the next deploy
that moves the image pin. The 5x401 typed refusal contract on the agent side
is unchanged ("P19-04: authentication refused 5 consecutive times (401) —
token is dead and no rotated token exists at <tokenfile>; refusing to loop
silently (agent exits)").

The false §2/§7 grace claims live in the closed artefact
/opt/data/tmp/probe1/P19-04_ROTATION.md and stand there as historical
record; this section is the corrected framing the repo carries.

## 4. /api/v1/settings TLS state — CA-file presence, not live verification (P18-04 §11.4, P19-05R F7)

The settings surface (P18-04, `internal/server/settings_api.go`) reports
TLS state strings `ok_plain_http` | `tls_without_client_ca` | `ok_tls_mtls`
and a `mtls_active` boolean. Qualifier (P19-05R F7, non-blocking visibility
gap): `mtls_active`/`ok_tls_mtls` reflect CA-file PRESENCE at boot, not live
handshake verification — expired/mismatched certs boot and still read
`ok_tls_mtls`. The state tells an operator the P19-01 wss dial refusal
pre-flight posture, not that a handshake would succeed today.
