# domain-knowledge — probe v1.19 (P8)

Source of truth: `inputs/HANDOVER_ORCH_PROBE_v1.19_20261123.md` (valmet, 23.11.2026, 48 h live plant use).

## Requirement matrix (P8-01, analyst)

Full matrix: `.workflow/analyst/P8-01_REQUIREMENTS.md` — 16 items (1.1, 2.1-2.3, 3.1-3.4, 4.1-4.4, 5.1-5.4),
8-test suite from §7, compatibility flags, 12 dependency edges, 8 explicit conflicts, §6 regression checklist.

Load-bearing facts for anyone touching probe v1.19:

- The field fleet is v1.18.5/v1.18.6 and cannot know v1.19. Anything that must reach the fleet rides
  `exec`, `fs-write`, `proc-start` (and `builds`) only. `POST /fs/upload` cannot deliver the first update.
- `fs-write` at 8 MB becomes ~10.7 MB base64 and was measured taking the server down — the same call is the
  only legal carrier for the update (§1 vs §2.3, conflict C1).
- Elevated work fails today at the token, not the group: agent runs as `flk`, filtered token `S-1-16-8192`.
  `Access is denied` on profile writes, `errore 5` on `tscon`, Access denied on `schtasks /Create /RU SYSTEM`.
- Cleanup must not live in the process that dies: a 280 s exec timeout killed a runner between `shutdown`
  and `no shutdown`, leaving PB1 of AP02R disabled 43 s on a true redundant pair.
- Exit evidence is unreliable: `fs-write` returned `ok` writing 0 bytes; a 186-byte placeholder replaced a
  280 KB ticket; `fs-list` hung 300 s; a nonexistent token's 7-line error passed a `>2 righe` validity gate.
- `exec` returns 200 with empty stdout on failure (§4.3) — and §6 simultaneously requires `exec` to behave
  as today (conflict C2). Same tension on `fs-write` refusal (C3).
- EAS1 clock is +10 h and no response declares `tz`.
- Items with no acceptance criterion anywhere: 4.4 (screenshot/input), 5.1 (capability discovery),
  5.3 (watch), 5.4 (idempotency). A coder cannot be held to them until valmet supplies one.
- valmet's short-time ranking (§9): 2.1 → 2.2 → §3. 1.1 is unranked but is the delivery gate (C4).
