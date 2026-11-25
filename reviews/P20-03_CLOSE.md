# P20-03 — CLOSE (written by the parent; the unit produced the artefact and no close record)

**Commit:** `8a78625` — the record commit that carries this close and the preflight artefact. The unit changed no tree file, so the verdict binds to the commit holding its evidence.
**Head-move waiver:** a read-only unit writes nothing, so it has no code head; binding to `8a78625` names the commit its evidence actually lives in rather than a tree it never touched. Verified: `git show --name-status 8a78625` is four `reviews/` paths and nothing else.
**Review:** `reviewed_by: PENDING` · `verdict: PASS` · `verdict_sha_of_head: 8a78625`
**Scope:** read-only reconnaissance across the Radxa (`radxa-e54c`, tailnet 100.71.4.28), the relay's netns, and central on KS-3 CT100. No container pulled, created, recreated, restarted or stopped; nothing written on any probe host. Mutations performed: **NONE**.

## Question the unit was asked

Can a Radxa deploy take EAS1 down?

## Answer: GO, scoped — and the scope is the finding

- **Q1 — the relay is its own container, not the probe host.** `:7800` is `probe-relayd` on the default docker bridge (`172.17.0.4`, docker-proxy on `172.17.0.3`), not a host process and not a process inside `probe-radxa`. A recreate set that assumed otherwise would take the relay down with the deploy.
- **Q2 — live config confirmed, and the spec's reading of `maxRetries:0` was WRONG.** The reconnect behaviour is not what the spec asserted; the observed configuration is recorded in the artefact. This is a correction to the written plan, not a defect in the host.
- **Q3 — if `maxRetries` is 0, nothing needs changing, and bounding it would make things worse.** The unit argues against the "obvious" fix rather than reaching for it.
- **Q4 — blast radius is one host, and EAS1 is untouched by construction.** The deploy path on the Radxa does not reach EAS1's identity or registry row.
- **VERDICT: GO** — scoped, with `probe-relayd` explicitly **excluded from the recreate set**.

The exclusion is the whole value of the unit: the deploy is safe only if the recreate set is named, and the name that must not appear in it is `probe-relayd`.

## Parent's verification

The artefact's claims are host observations from a read-only vantage, so the parent verified the parts that reach this repository: no tree file changed, `git status` clean, and the unit's own constraint (no mutations) is consistent with the absence of any deploy/restart artefact or audit row for those hosts in the window. The host-level state itself is taken as read-only reported evidence, labelled as such — the parent did not re-run ssh probes to duplicate it.

## Open items

- **`probe-relayd` must be excluded from any recreate set.** Written into the rollout unit's constraint, not left in this document — a safety property that lives only in prose gets missed by whichever agent writes the recreate command.
- **P16-02 stays blocked, and this unit narrows nothing.** A pull-and-run on the Radxa is a mutation; this preflight was read-only and does not constitute the authorisation P16-02 needs. The arm64 execution claim therefore remains unproven.
- The `maxRetries:0` correction (Q2/Q3) should reach whoever owns the deploy spec; the artefact records it, but the spec still says otherwise.
