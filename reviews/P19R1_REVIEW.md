# REVIEW — P19R1 falsify unit (P19-05R): P19-04 rotation + P18-04 /settings at HEAD d4ddc88

VERDICT FIX-FIRST — P19-04 (F1 blocking: rotatedTokens restart-orphan) · P18-04 CONFIRMED with one non-blocking visibility finding (F7 expiry-blind)
reviewed_by: reviewer
verdict_sha_of_head: d4ddc88f8387fbf735206d6f81d805a3c1ef27b4 (re-verified on arrival; deposition of this unit's own probes by parent = 865ab70, no product code committed by reviewer)
artefact: /opt/data/tmp/probe1/P19R1_FALSIFY.md (full verdict table, evidence classes, unattacked-claims record)

## Verdict summary

P19-04 (f34934c): FIX-FIRST.
- F1 BLOCKING — supersession-cut holds in memory (agent_token_api.go:112-114, server_token.go:133-140) but rotatedTokens is memory-only (server.go:100-102, 298-302; zero persistence sites). A server restart (the artefact's own §7 fleet action is exactly a compose restart) empties the slot; an agent presenting its rotated token then bricks: pickupRotatedToken cannot help (its file already holds the rotated value → tok==current → not a pickup, agent.go:1952-1954; the pre-rotation token was discarded at adoption, agent.go:436-439) → 5x401 typed exit. Live-verified: probe TestP19R1_F1_F2_NoRecoveryAfterLocalRotation — post-rotation agent vs auth-refusing peer exits unrecovered. The §2 "bounded one-interval orphan life" claim is false across restart: the orphan is unbounded and the failure is the very "unrecoverable without console access" state a cut was rejected for. Fix: persist rotatedTokens (registry/enrollment-db) or re-seed on reconnect.
- F2 non-blocking — pickup adopts any differing non-empty content with no material gate (agent.go:1941-1956); persistToken is non-atomic os.WriteFile (agent.go:1555-1560). Partial/interleaved write or operator hand-replace bricks the agent (see F1 evidence: no local recovery). Fix: temp+rename + charset gate.
- F1c/F1d non-blocking — store-before-deliver inversion vs proactive path (agent_token_api.go:113 before :120 vs server_token.go:212-218); proactive rotation paused while agent offline (server_ws.go:380-392 reseeds on rotate result only).
- F3 horntight-as-claimed — reset is on ANY successful connection (agent.go:472); live alternator 865 dials no-exit; consecutive case exits typed at 5 (both probes live). Advisory only.
- F4 horntight — plaintext confined to the one-hop envelope marshal (agent_token_api.go:99-108); logs/audit/responses carry token_fp only (rows verified by read at HEAD).

P18-04 (d4ddc88): CONFIRMED, one non-blocking finding.
- F5 horntight — settings reads the same fields behaviour reads (tokenTTL: settings_api.go:127-129 == server_token.go:69; cert/key/CA paths: settings_api.go:120-126 == listener binding; relay allowlist object identical, settings_api.go:116 → relay_identity.go:571-582).
- F6 horntight — payload typed secrets-free (settings_api.go:80-106); free-form strings are constants/enums/sha256[:8] fps (settings_api.go:130, 53-75; relay_identity.go:470-489, 81-85); live capture HTTP 200 from TLS+mTLS rig, fp-only body. mTLS enforcement itself live-verified (no-client-cert curl fails handshake; client-cert 200).
- F7 non-blocking — visibility INCOMPLETE: an EXPIRED server servercert (NotAfter 2026-10-28) boots and serves; settings reports ok_tls_mtls / mtls_active=true while agent-style wss dial is refused x509-expired (live rig :18795, WSS_DIAL_AGENTSTYLE). Unreadable CA and cert/key mismatch are boot-fatal (no hole). Fix: NotAfter freshness in the tls state row.

## Evidence classes
F1/F2/F3: E2 (reviewer-built + run in this unit; probes committed as 865ab70 by parent capture, untracked at review start). F5/F6/F7/F4 rows: E1/E2 mixed per-row table in the artefact. Named-fail set (P2107, P2108x2) not chased per brief.

Full per-finding method/evidence/verdict table: /opt/data/tmp/probe1/P19R1_FALSIFY.md
