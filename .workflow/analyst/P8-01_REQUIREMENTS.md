# P8-01 — probe v1.19 requirement matrix (spec → measurable matrix)

profile: analyst · gateway: gateway-analyst (s6-supervise, pid 1063648) · status: ACTIVE
workflow: wf_probe1 · milestone P8 · unit P8-01 · owner analyst
input: inputs/HANDOVER_ORCH_PROBE_v1.19_20261123.md (valmet, 23.11.2026, 217 lines, 10,352 B)
input sha256: eab36245d39231add88d72b98ba9f661f98f8a776d9e79fe614a9bf2fe9be38f (10352 B)
mode: requirements only — no design, no code, no fix.
research leg: NONE (the spec is the input; probe source not read in this unit)

Numbering note: valmet numbers only §2 (2.1-2.3) and §4 (4.1-4.4). Items in §1, §3 and §5 are labels 1.1,
3.1-3.4, 5.1-5.4 assigned HERE, one per clause valmet wrote; the clause text is quoted, not paraphrased.

---

## A. Matrix — one row per item, sections 1-5

### A.1 §1 non-negotiable constraint

| id | title | field incident (quoted measurement) | acceptance criterion (verbatim) | API surface added/changed | v1.18.5/1.18.6 peer compat |
|----|-------|-------------------------------------|--------------------------------|---------------------------|----------------------------|
| 1.1 | Live-update must ride the OLD client's API | field state: server `v1.18.6`, agent `V6049A-EAS1` `v1.18.6`, exe `8.040.448 B`, sha `44e179b115329ea7…`, backup `.exe.old` = `1.18.5`. Version anchor: "oggi l'unico appiglio è il campo versione riportato, ed è fragile". Delivery cost: "per aggiornare l'agente ho dovuto fare tre hop (`scp` → `pct push` → `docker cp`)" (§2.3) | "da un agente v1.18.6, con una sola sequenza di chiamate già note, si arriva a v1.19 e `GET /api/v1/agents` mostra la nuova versione, con `.exe.old` presente e hash verificato." | NONE new, by definition. Uses `fs-write` + `exec` + `proc-start` (+ `builds` named in §1 line 26). Readback `GET /api/v1/agents` (existence assumed from the criterion text — NOT CHECKED, §G) | YES on the wire — v1.18.6 keeps calling only what it knows. Constraint holds only if the new exe is deliverable on `fs-write`; see C1 |

### A.2 §2 P0 — the three biggest deviation generators

| id | title | field incident (quoted measurement) | acceptance criterion (verbatim) | API surface added/changed | peer compat |
|----|-------|-------------------------------------|--------------------------------|---------------------------|-------------|
| 2.1 | Elevated (SYSTEM) channel — "la prima cosa da fare" | agent runs as `flk`, "non in nessun gruppo admin", filtered token `S-1-16-8192`; writes to Windows profiles "accent colour / tema per 6 profili × 9 stazioni" → `Access is denied`; `tscon <id> /dest:console` → "errore 5, anche come proprietario"; `schtasks /Create /RU SYSTEM /RL HIGHEST` → Access denied; workaround "sshd locale + `askpass.exe` compilato a runtime + task SYSTEM … fragile e va rifatto su ogni macchina" | "da chiamata standard si scrive una chiave in `HKLM`, si crea un task SYSTEM e si esegue `tscon`, senza SSH e senza chiedere password." | `exec {as: "SYSTEM"}` (new param) — or agent installed as Windows service, "con output nella stessa forma degli altri exec" | YES — additive param; old peers never send `as`. Service variant changes agent start mode → `proc-start` bootstrap path must survive (C6) |
| 2.2 | Detached + guaranteed cleanup | "runner del port test ucciso a metà → `A1-SWI15 Gi1/1` lasciata `disabled`, collateral `notconnect` su `A1-SWI14 Gi1/2`"; "exec timeout a 280 s uccide il runner fra `shutdown` e `no shutdown` → PB1 di AP02R `disabled` per 43 s su una coppia ridondata vera"; "Il `finally` di PowerShell non gira se il processo viene terminato" | "lancio un job che lascia una porta giù e poi uccido l'agente. Il server esegue `on_death`, la porta torna su. Ripetibile." | `exec {command, detach: true, on_death: "<comando>"}` → `{pid, started_at}`; `job_status {pid}` → `{alive, exit_code, last_line, age_s}`; `job_log {pid, since_offset}` → incremental tail. `on_death` executed BY THE SERVER on non-zero exit or agent death | YES — additive; old peers omit `detach`. New server must still serve old agents' plain exec. Needs server-side job identity (D2, D3) |
| 2.3 | Streamed upload + binary download | "`fs-write` in base64: un exe da 8 MB diventa ~10,7 MB di base64 e **fa cadere il server**"; "tre hop (`scp` → `pct push` → `docker cp`)"; "Ogni screenshot è base64 dentro JSON"; "`fs-read` accetta `offset`/`limit` (oggi non li ha: su file grandi è un problema reale)" | "carico 50 MB senza base64 e senza cadute; scarico un PNG in 3 chunk con resume dopo una disconnessione forzata." | `POST /fs/upload` (new: raw body, chunk + resume); `GET /fs/download` (new: `Range`); `fs-read` gains `offset`/`limit` (change) | PARTIAL — `fs-read` change is additive if optional; new endpoints invisible to old peers. v1.18.6 cannot call `/fs/upload`, so it cannot serve 1.1 (C1) |

### A.3 §3 P0 integrity — "mai un `ok` senza prova" (clauses split, see numbering note)

| id | title | field incident (quoted measurement) | acceptance criterion (verbatim) | API surface added/changed | peer compat |
|----|-------|-------------------------------------|--------------------------------|---------------------------|-------------|
| 3.1 | `sha256` + `size` in EVERY `fs-*` response | "`fs-write` risponde `ok` e scrive **0 byte**"; "un placeholder da **186 byte** ha sostituito un **ticket da 280 KB**; l'originale restava solo nel cestino" | "un write da 0 byte è un errore, non un `ok`." (§7 T5) | response fields `sha256`, `size` on every `fs-*` call | YES if additive; old-client tolerance of unknown JSON fields is UNVERIFIED (HYPOTHESIS, §G) |
| 3.2 | `truncated: true` explicit on `fs-read` | "`fs-list` appeso **300 s** senza errore"; "un token inesistente risponde `cannot connect to application server` in **7 righe** e passa una soglia `>2 righe` come lettura valida — un nodo scomparso registrato come 'log letto'" | "Un read troncato lo dichiara." (§7 T5) | `fs-read` response field `truncated` | YES — additive field |
| 3.3 | `fs-write` refuses when expected byte count ≠ actual | "`fs-write` risponde `ok` e scrive **0 byte**"; placeholder `186 byte` vs ticket `280 KB` | "un write da 0 byte è un errore, non un `ok`." (§7 T5); ask: "`fs-write` rifiuta se il byte count atteso non corrisponde" | `fs-write` gains expected-count parameter + refusal error | NO / CONDITIONAL — refusal changes today's behaviour and old callers send no expected count → collides with §6 (C3) |
| 3.4 | Per-call deadline + structured error instead of hang | "`fs-list` appeso **300 s** senza errore"; "un 503 è durato **ore** con riconnessione non osservabile" | "Un `fs-list` appeso torna errore entro la deadline." (§7 T5) | per-call deadline; structured error object | CONDITIONAL — only failure paths change; must not apply to detached jobs (C5) |

### A.4 §4 P1 — elegance that becomes robustness

| id | title | field incident (quoted measurement) | acceptance criterion (verbatim) | API surface added/changed | peer compat |
|----|-------|-------------------------------------|--------------------------------|---------------------------|-------------|
| 4.1 | Credential vault + SSH helper | "Metà del tempo è andato in quoting che collassa (ssh → PowerShell → comandi sullo switch), `-EncodedCommand` in UTF-16LE, `askpass` compilato a mano, password da passare per variabile d'ambiente perché il command line è visibile" | "eseguo una sequenza su uno switch con credenziale referenziata; la password non compare in nessun log né nel risultato." | `exec {credential: "sw-enable"}`; server injects secret into the process "mai in command line, mai in chat, mai su disco"; plus SSH helper handling banner, `>` vs `#` prompt, stdin (helper surface unspecified — GAP) | YES — additive; requires new server-side secret store; v1.19-only on the agent side |
| 4.2 | Named resource lock | "Nulla impedisce due runner. Due consumatori sulla stessa coppia possono riavviare **entrambe le metà** e lasciare l'impianto senza ridondanza" | no inline §4.2 criterion; §7 T7 only: "`lock` su `switch:A1-SWI07:Gi1/4` impedisce il secondo job" | `lock(resource, ttl)` named; explicit refusal "risorsa già in uso dal job X"; resources `switch:A1-SWI07:Gi1/4`, `node:AP02:RESERVE` | YES — additive; needs server-side lock registry + TTL expiry |
| 4.3 | Structured envelope + declared time | "`exec` torna **200 con stdout vuoto** anche su fallimento"; "un 503 è durato **ore** con riconnessione non osservabile"; "l'orologio di EAS1 è **+10 h** e le risposte non dichiarano il fuso" | no inline §4.3 criterion; §7 T8 only: "envelope completo su comando fallito, con `tz` dichiarato" | every response: `{exit_code, stdout, stderr, duration_ms, started_at_utc, started_at_local, tz}` "sempre, anche su errore"; "Nessun 200 che significhi due cose diverse" (status semantics change) | NO — 200-as-success callers break; direct collision with §6 (C2) |
| 4.4 | Primitives for visual proof | "L'evidenza richiesta dall'utente è **screenshot**. Oggi li costruisco a mano (`PrintWindow`, `CopyFromScreen`) e pilotare una finestra Qt è un lavoro in sé (RFB con pointer event big-endian, coordinate ricalibrate a ogni cambio di geometria)" | NONE STATED — GAP (no inline criterion, no §7 test) | `screenshot {window \| region}` → PNG "via download, non base64"; `input {click \| keys \| focus}` with state readback | YES in principle — but PNG-over-download depends on 2.3 (D5) |

### A.5 §5 P2 — platform hygiene (all four: NO acceptance criterion in the spec)

| id | title | field incident (quoted measurement) | acceptance criterion | API surface added/changed | peer compat |
|----|-------|-------------------------------------|----------------------|---------------------------|-------------|
| 5.1 | Capability discovery | "indovino ogni volta i path (`BsTool`, `rfb_node.ps1`, `hosts`, `System Configurator`)" | NONE STATED — GAP | new endpoint "che dice cosa c'è su quell'agente" (shape unspecified) | YES — additive |
| 5.2 | Agent as a Windows service | "oggi vive dentro una sessione RDP; chiuderla lo uccide"; "È anche un requisito nave esplicito" | NONE inline — GAP; only overlaps §7 T1 (install/restart) and is the alternative branch of 2.1 | service install + auto-restart + status console (surface unspecified) | CONDITIONAL — `proc-start` bootstrap path must keep working or 1.1 breaks (C6) |
| 5.3 | `tail`/`watch` | "leggo i progress in polling con `fs-read`" | NONE STATED — GAP | `watch {path, since}` → new lines | YES — additive; incremental semantics ride 2.3's `fs-read` offsets (D6) |
| 5.4 | Idempotent retry | "`exec` con `idempotency_key` per non rieseguire un comando che è già andato" (no measurement given) | NONE STATED — GAP | `exec {idempotency_key}`; server-side replay record | YES — additive; needs the 2.2 job identity (D7) |

Count: 16 items — §1:1 · §2:3 · §3:4 · §4:4 · §5:4.
Items with NO acceptance criterion anywhere: 4.4, 5.1, 5.3, 5.4 (4 of 16). Items with criterion only in §7, none inline: 4.2 (T7), 4.3 (T8).

---

## B. Suite — valmet's EIGHT acceptance tests, expanded to observable PASS/FAIL

Global precondition: field agent `V6049A-EAS1` reachable, server `v1.18.6` (or candidate build) running; plant-side
targets available (PCS node, `A1-SWI07`, `A1-SWI15`, `A1-SWI14`, `AP02R` PB1). Every test is run on a live plant —
preconditions include "a change of state is restorable by the test's own cleanup".

| # | spec test (verbatim) | precondition | action | observable PASS | FAIL signature |
|---|----------------------|--------------|--------|-----------------|----------------|
| T1 | "aggiornamento v1.18.6 → nuova, solo con API vecchie, hash verificato, `.exe.old` presente" | agent at `v1.18.6` (exe `8.040.448 B`, sha `44e179b115329ea7…`), `builds` readable, `proc-start` permitted | one sequence of already-known calls only (`fs-write` delivery + `exec` rename/restart + `proc-start` or equivalent) | `GET /api/v1/agents` reports the new version; `.exe.old` present on disk; artifact hash equals the built hash; agent re-registers under the SAME name `V6049A-EAS1` | any new endpoint in the call path; version unchanged or read from build label; no `.exe.old`; hash mismatch; agent gone after restart; agent re-registers under a different name |
| T2 | "`exec {as:"SYSTEM"}`: scrittura HKLM + task SYSTEM + `tscon`, zero SSH" | agent running as `flk`, non-admin, filtered token `S-1-16-8192`; an RDP session id exists for `tscon` | one standard call performing: HKLM key write, `schtasks /Create /RU SYSTEM /RL HIGHEST`, `tscon <id> /dest:console` | all three effects observable (key readable, task listed, session detached to console); no SSH, no `askpass`, no password prompt anywhere in the transcript | `Access is denied` / `errore 5`; any SSH hop or runtime-compiled `askpass.exe` in the path; success reported without the key/task/session state actually changing |
| T3 | "`on_death`: porta lasciata giù + agente ucciso → porta ripristinata dal server" | a job can take a switch port down and leave it down; `on_death` command restores it; agent kill is permitted | start detached job that disables a port, then kill the agent process mid-flight | server executes `on_death`; port is back up; run repeated ≥2x with the same outcome | port stays `disabled`; cleanup ran only while the agent was alive; second run differs; `job_status`/`job_log` cannot report the job after agent death |
| T4 | "upload 50 MB senza base64; download PNG con `Range` e resume" | 50 MB file; a PNG on the agent; ability to force a disconnect | raw-body upload of 50 MB; then download the PNG in 3 ranged chunks with a forced disconnect between chunks | 50 MB lands with matching size and hash, server stays up; PNG delivered in 3 chunks, resume continues from the last byte, final PNG opens and hash matches | server drops/hangs; any base64 encoding in the path; resume restarts at 0 or produces a corrupt PNG; `Range` ignored |
| T5 | "`fs-write` 0 byte → errore; `fs-read` troncato dichiarato; `fs-list` appeso → errore entro deadline" | writable path; a file larger than the read limit; a path that induces the observed hang (unreachable/slow share) | three calls: zero-byte write with declared expected count; oversized read; `fs-list` on the hanging path | write returns a structured error, file unchanged; read carries `truncated: true`; `fs-list` returns a structured error within the deadline | `ok` on a 0-byte write; silent truncation without the flag; hang ≥300 s with no error |
| T6 | "credenziale referenziata su switch: nessun segreto in log/risultato" | a stored credential (`sw-enable` class) and a switch reachable via the SSH helper | run a multi-step switch sequence referencing the credential | sequence executes; grep of server log, job log and returned result finds no secret value; process command line carries no secret | secret appears in command line, server log, job log, result body or transcript; credential unusable without it being echoed |
| T7 | "`lock` su `switch:A1-SWI07:Gi1/4` impedisce il secondo job" | two clients/jobs able to touch the same resource name | job A takes `lock("switch:A1-SWI07:Gi1/4", ttl)`; job B requests the same resource | B is refused with the explicit message naming the holder ("risorsa già in uso dal job X"); A's lock releases on completion or TTL | both jobs run; refusal without naming the resource/holder; lock never expires after a dead holder |
| T8 | "envelope completo su comando fallito, con `tz` dichiarato" | a command guaranteed to fail (non-zero exit) on the node | issue that command and read the response | response carries `exit_code` (non-zero), `stdout`, `stderr`, `duration_ms`, `started_at_utc`, `started_at_local`, `tz`; the transport status does not read as success | any field missing; `tz` absent; 200 meaning both success and failure; empty stdout with a 200 and no `exit_code` |

Coverage of the suite: T1←1.1 (+3.1/3.2/3.4 as the hash and integrity machinery it relies on) · T2←2.1 · T3←2.2 (+5.2 for post-death execution) · T4←2.3 · T5←3.1/3.2/3.3/3.4 · T6←4.1 · T7←4.2 · T8←4.3.
Items with NO test at all: 4.4, 5.1, 5.3, 5.4 (4 of 16) — same set that has no criterion.

---

## C. Items that cannot be satisfied inside §1 (old-API-only) — flags

Constraint restated (verbatim, §1): "il live-update deve funzionare **sull'API che il client in campo parla già** — `exec`, `fs-write`, `proc-start`, `builds`. Non si può introdurre un endpoint nuovo e pretendere che il parco lo usi per aggiornarsi".

| item | flag | what the constraint does to it |
|------|------|-------------------------------|
| 2.3 | CANNOT SERVE §1 | `POST /fs/upload` / `GET /fs/download` are unknown to v1.18.6. The 8 MB exe must still travel on `fs-write`. 2.3 helps v1.19→v1.20 only. Collides with 1.1 (C1) |
| 1.1 | AT RISK, not impossible | Its only legal carrier is `fs-write`, the very call measured at "8 MB → ~10,7 MB base64 … fa cadere il server". §1's acceptance is unreachable unless `fs-write` itself can take the artifact in pieces without a new endpoint (§1 forbids one). This is the single hardest constraint intersection in the spec |
| 4.4 | PARTIAL | "PNG (via download, non base64)" requires 2.3's download endpoint; a v1.18.6 peer has no such transport. Feature is v1.19-only by construction |
| 5.2 | PARTIAL / chicken-and-egg | Installing a Windows service needs the elevation the agent does not have (`S-1-16-8192`, `Access is denied` on `schtasks /Create /RU SYSTEM`). It can only be installed after 2.1 exists, or by the manual `askpass`+SSH path valmet wants to delete (C6) |
| 4.1 | agent-side only | Server-side secret injection needs no new peer capability, but `exec {credential}` is only honoured by a v1.19 agent → the bootstrap hop cannot use it (that hop is exactly a large file move, which is fine) |
| 5.4 | needs server state | `idempotency_key` is only meaningful if the server keeps a replay record; an old agent cannot dedupe by itself |
| others (2.1, 2.2, 3.1-3.4, 4.2, 4.3, 5.1, 5.3) | SATISFIABLE | additive or failure-path-only; none requires the field fleet to know v1.19 |

Items fully clean inside the constraint: 2.1, 2.2, 3.1, 3.2, 3.3, 3.4, 4.2, 4.3, 5.1, 5.3 (10). Partial: 4.1, 4.4, 5.2, 5.4 (4). Cannot serve the bootstrap: 2.3 (1). Gate at risk: 1.1 (1). Total 16.

---

## D. Priority trio (§9) and dependency ordering

### D.1 valmet's "se il tempo è poco" ranking — verbatim

| rank | item | spec line |
|------|------|-----------|
| 1 | elevated channel (2.1) | §9 "1. **canale elevato** (2.1)" |
| 2 | detached + on_death (2.2) | §9 "2. **detached + on_death** (2.2)" |
| 3 | fs-* integrity (§3) | §9 "3. **integrità fs-*** (3)" |

§9 justification (verbatim): "Con questi tre, le ultime 48 ore sarebbero state la metà del lavoro e zero incidenti sull'impianto."
Note: §9 does NOT rank 1.1 — it is not optional, it is the delivery gate (C4). §3 in this matrix = 3.1-3.4.

### D.2 Dependency edges

| edge | dependency | basis | risk if violated |
|------|-----------|-------|------------------|
| D1 | 1.1 → 3.1 + 3.3 (+3.4) | §1 requires "verificato con hash"; §3 measures `fs-write` returning `ok` on 0 bytes | an update that "succeeds" on a truncated exe bricks the agent with no rollback evidence |
| D2 | 2.2 → server-side job registry | `job_status {pid}`, `job_log {pid,…}` need pid→job identity that survives the agent process; `on_death` must fire after agent death | `job_status` returns nothing after the exact event it exists for; `on_death` silently never runs |
| D3 | 2.2 → 5.2 (or a supervisor not named in the spec) | "`on_death` viene eseguito **dal server** … se l'agente muore" — a dead process on the host executes nothing | T3 passes only if something restarts the agent; otherwise the criterion is unachievable (C8) |
| D4 | 2.1 → 5.2 | service install requires elevation the agent lacks today (`S-1-16-8192`) | the service branch of 2.1 is blocked until `exec {as:"SYSTEM"}` exists (C6) |
| D5 | 4.4 → 2.3 | "PNG (via download, non base64)" | screenshot has no transport; falls back to base64-in-JSON, the thing §2.3 calls a server-killer |
| D6 | 5.3 → 2.3 (`fs-read` offset/limit) | incremental tail needs ranged reads; today `fs-read` has no `offset`/`limit` | watch re-reads whole files (the current polling cost) |
| D7 | 5.4 → 2.2 registry | an idempotency key must resolve to a job identity stored server-side | duplicate execution of an already-run command — the exact field hazard |
| D8 | 3.1/3.2 → 4.3 (one response-shape decision) | both mutate the same `fs-*` body: sha256/size (§3) and the always-envelope (§4.3); §4.3 also changes status semantics | two independent envelope edits → inconsistent responses between `fs-*` and `exec` |
| D9 | 3.4 ↔ 2.2 | the 280 s exec timeout is the measured cause of the PB1/AP02R outage; §3 wants a per-call deadline | the deadline re-creates the incident by killing a detached job mid-sequence (C5) |
| D10 | 2.3 ↛ 1.1 | upload/download are not in the old peer's vocabulary | treating 2.3 as the update path makes T1 fail by construction (C1) |
| D11 | 4.1 → new server-side secret store | secret must never touch command line, chat or disk | credential either leaks or the feature is a wrapper with no secret |
| D12 | 4.2 → server-side lock registry + TTL sweep | "rifiuto esplicito" plus `ttl` implies a holder that can die | stale lock blocks the plant; or no second-job prevention at all |

Independent of everything above: 5.1 (discovery), 3.2 in isolation, 4.3's `tz` field content.

### D.3 Ordering implied by the edges (facts only, no solution choice)

| order | items | why |
|-------|-------|-----|
| 1 | 2.1 (`exec {as:"SYSTEM"}` branch) | valmet rank 1; unblocks 5.2 and removes the `askpass` path |
| 2 | 3.1/3.2/3.3/3.4 | prerequisite of 1.1's hash evidence; valmet rank 3 |
| 3 | 1.1 (bootstrap on old API) | delivery gate — nothing else reaches the field before it works; needs 3.x (D1) and a `fs-write` carrier that does not collapse (C1) |
| 4 | 2.2 (+ D2 registry, D3 supervisor) | valmet rank 2; requires the registry and a post-death executor to satisfy T3 |
| 5 | 5.2 (service) | after 2.1 (D4); satisfies the ship requirement and D3's supervisor need |
| 6 | 2.3, then 4.4, 5.3 | 2.3 is additive and only v1.19+; 4.4/5.3 depend on it (D5, D6) |
| 7 | 4.1, 4.2, 4.3, 5.1, 5.4 | 4.3 shares the envelope decision with 3.x (D8); 5.4 rides 2.2's registry (D7) |

---

## E. Conflicts — stated, not resolved

| # | conflict | parties | evidence |
|---|----------|--------|----------|
| C1 | Bootstrap carrier vs measured `fs-write` limit | §1 ("solo `fs-write` + `exec` + `proc-start`") vs §2.3 ("8 MB → ~10,7 MB … fa cadere il server") | the only legal path for the exe is the path measured to crash the server at exactly that payload |
| C2 | `exec` must both "behave as today" and stop meaning two things with 200 | §6 ("`exec` … devono continuare a esistere e comportarsi come oggi") vs §4.3 ("Nessun 200 che significhi due cose diverse") | both are P0-binding; a status-semantics change is observable by v1.18.5/1.18.6 callers |
| C3 | `fs-write` must both "behave as today" and refuse on count mismatch | §6 vs §3 ("`fs-write` rifiuta se il byte count atteso non corrisponde") | old callers send no expected count, so the refusal is either opt-in (and then §3's protection is conditional) or a behaviour change |
| C4 | Priority ranking vs delivery gate | §9 ranks 2.1 first; §1 makes 1.1 the only way anything ships | building 2.1/2.2/§3 without 1.1 yields no field deployment; building 1.1 first contradicts the stated order |
| C5 | Detached lifetime vs per-call deadline | §2.2 (job outlives the call; agent may die) vs §3 (deadline per call, error instead of hang) | the 280 s timeout that caused the 43 s PB1 outage is a per-call deadline — applying it to detached jobs reproduces the incident |
| C6 | 2.1's two branches are not equal | §2.1 "`exec {as:"SYSTEM"}` — oppure agente installato come servizio" | service install needs elevation that does not exist yet (`schtasks /Create /RU SYSTEM` → Access denied), so the "or" is really "first, then" |
| C7 | Build repo path vs worktree rule | §6 names `/opt/data/workspace-operative/probe`; the workflow builds in worktree `/opt/data/projects/PROBE_UPGRADE/worktree` (branch `probe-upgrade-w1`) | §6's own escape clause covers staging only ("se cambia, serve documentarlo"), not the repo path — needs a documented decision |
| C8 | `on_death` after agent death needs an executor on a host whose agent is dead | §2.2 ("`on_death` viene eseguito dal server … se l'agente muore") vs the fact that the server cannot execute on that host without a process there | unstated dependency on 5.2 / a supervisor; without it T3 cannot pass (D3) |

Cross-check: C2 and C3 are the only conflicts where §6 and a P0 requirement both bind the same call. C1 is the only one that can make §1's own acceptance unreachable.

---

## F. Regression checklist — §6 "Cosa NON rompere"

| # | invariant (verbatim) | check | fail looks like |
|---|----------------------|-------|-----------------|
| R1 | "`exec`, `fs-write`, `fs-read`, `fs-list`, `fs-stat`, `fs-hash`, `proc-list`, `proc-start`, `tunnel`, `update` devono continuare a esistere e comportarsi come oggi" | one smoke call per endpoint from a v1.18.6-shaped client; response shape + status semantics compared to today | any endpoint removed/renamed; a v1.18.6 call path now errors or changes meaning (see C2/C3) |
| R2 | "Formato dei path: gli script in campo usano `C:\temp\...` e `D:\dna\Shared\Runtime\...`" | read+write+list against both path shapes | backslash normalisation, path rewriting, or rejection of the field formats |
| R3 | "Non introdurre dipendenze (Python non esiste sui nodi; PowerShell 5.1 sì)" | artifact inventory for the agent binary; no interpreter/runtime added | any Python/node/embedded runtime on the node; PS 7-only syntax |
| R4 | build: "`GO=/opt/data/sdk/go-mantle-fork/bin/go`, config JSON in base64 via `-ldflags "-X main.configB64=..."`" | build from HEAD with that exact GO and ldflags; binary runs with the injected config | a different toolchain; config passed another way; non-reproducible HEAD build |
| R5 | staging: "passa da `operator-op01-probe-1` in CT100 — se cambia, serve documentarlo" | verify staging host + document any change | silent staging change |
| R6 | "la versione si legge dall'artifact, mai dalla label del build" | version string in `GET /api/v1/agents` cross-checked against the binary artifact | version taken from build label/config; label and artifact disagree |

Endpoint count in R1: 10, as listed by valmet. R6 restates §1's own rule (lines 33-34) — it is both a constraint and a regression item.

---

## G. Evidence state / not checked

| claim class | state |
|-------------|-------|
| every incident quote, every acceptance criterion, every priority ranking | quoted from the input file (217 lines); no paraphrase in the criterion column |
| `GET /api/v1/agents` exists and reports version | ASSUMED from §1's criterion text — NOT CHECKED against code |
| v1.18.5/1.18.6 tolerance of additive JSON fields; reliance on 200-as-success; strictness on `fs-read` params | HYPOTHESIS — probe source not read in this unit (research leg NONE). Compat column is therefore "what changes on the wire", not a parser audit |
| §8 (phases declared vs executed; `start`→`end` in 40 ms) | OUTSIDE the matrix scope (§1-5 per brief); valmet calls it "una nota che non è del client"; no id, no criterion, not booked as an item |
| dependency edges D2, D3, D7, D11, D12 | INFERENCE from the stated acceptance criteria, not stated by valmet (labelled in the basis column) |

NOT CHECKED: probe source at branch `probe-upgrade-w1` → the wire-compat flags (additive-field tolerance, 200-as-success reliance, `fs-read` param strictness) are unverified; the matrix is spec-faithful, not code-verified.

---

## H. Close record

| field | value |
|-------|-------|
| input | inputs/HANDOVER_ORCH_PROBE_v1.19_20261123.md · sha256 `eab36245d39231add88d72b98ba9f661f98f8a776d9e79fe614a9bf2fe9be38f` · 217 lines · 10,352 B |
| artefact | /opt/data/tmp/probe119/P8-01_REQUIREMENTS.md |
| sync | .workflow/analyst/P8-01_REQUIREMENTS.md (worktree `probe-upgrade-w1`) + .workflow/domain-knowledge.md pointer |
| commit | worktree `probe-upgrade-w1`: the commit carrying this file mentions P8-01 (byte-identical copy under .workflow/analyst/); sha reported in the unit report, not in-file |
| open item | C1 — §1's bootstrap acceptance and §2.3's measured `fs-write` collapse share the same carrier; valmet's §9 ranking also omits 1.1 (C4). Both need a decision above the coder |
| counts | 16 items · 8 tests · 4 items with no criterion and no test (4.4, 5.1, 5.3, 5.4) · 8 conflicts · 12 dependency edges · 6 regression rows · 3 ranked |
