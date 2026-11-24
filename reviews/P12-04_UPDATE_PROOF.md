VERDICT FIX-FIRST /opt/data/tmp/probe119/P12-04_UPDATE_PROOF.md bbaacbb 541 pass / 0 fail / 2 skip (top-level; 618 incl. subtests)

# P12-04 — one real update, Linux headless

reviewed_by: reviewer
verdict: FIX-FIRST
verdict_sha_of_head: bbaacbbcdce4c24b6b408b5a85cf1dd62d676a75
suite: go test ./... -count=1 at bbaacbb -> 541 top-level PASS / 0 FAIL / 2 SKIP (77 subtests also pass; 618 test events total). Skips: internal/server TestDownloadURL_LiveCrossProcessProof, TestV1_CreateTransfer.
repo: /opt/data/projects/PROBE_UPGRADE/v119wt, branch r119-build
date: 2026-11-24

Method note: router reading during the run pointed at an unrelated prior unit
(M36-09_FIX.md); not followed — the evidence below is execution of the real
binaries on this host. Units under test were not edited.

## Builds used (E1)

    source 0cc26e4 (P11-05, pre-P12-01)  -X version=1.18.5   15216426 B  sha256 2151bde1760a8d83e80b13041f12c400325105f7b4ed19cb96f899635f17def3
    source bbaacbb (HEAD, r119-build)    -X version=1.19.1   15343695 B  sha256 d03769e5c2fc18833a1f80407c282f8ac7fbe7a6b694e38207a7d8deef752856
    server bbaacbb                                     v1.19.1, 127.0.0.1:17700, isolated --files-dir/--registry

    $ probe-agent-old --version   -> PROBE v1.18.5
    $ probe-agent-new --version   -> PROBE v1.19.1
    $ probe-server-new --version  -> PROBE Server v1.19.1

## Acceptance table

| check | evidence | result |
|---|---|---|
| Real update driven through the API | `POST /api/v1/agents/p1204-agent/update {"binary_path":"probe-agent-new","version":"1.19.1"}` -> HTTP 200 `{"status":"update_in_progress"}`; canary log 22:25:39 -> 22:25:50 | PROVEN (only after F1 workaround) |
| API update works for a stock agent | same POST on p1204-noupd (default caps) -> HTTP 200 `update_in_progress` in **0.089 s**; agent log has no `[update]` line; directory unchanged; registry still 1.18.5 | **REFUTED** (F1) |
| Process now runs the new build, version read from the artifact | `$ run/agent/probe-agent --version` -> `PROBE v1.19.1`; canonical 15343695 B sha256 d03769e5…; **running** pid 2246668: `readlink /proc/2246668/exe` -> `…/run/agent/probe-agent`, `sha256sum /proc/2246668/exe` -> d03769e5… | PROVEN |
| Old binary parked at the swap's backup name with pre-swap size+sha | `run/agent/probe-agent.old` 15216426 B, sha256 2151bde1…; pre-swap values were 15216426 B / 2151bde1…; canary log: `backup …/probe-agent.old size=15216426 sha=2151bde1760a8d83` | PROVEN |
| Old process stopped after commit | pid 2246591 `ps` -> gone; new pid 2246668 alive | PROVEN |
| Same-name re-registration | registry `p1204-agent 1.19.1 active`, `connection_started_at=2026-11-24T22:25:50Z`, `reconnects=2`; no `p1204-agent-canary` row remains; canary log: `Disconnected, reconnecting… Connected.` at 22:25:50 | PROVEN |
| Health/registry row agrees with what the agent reports | registry/health `version=1.19.1`, `status=active`, `stale=false`, `last_seen_age_seconds=2`; agent status record `version=v1.19.1` | PROVEN on version/state; **REFUTED on name+executable** (F2) |
| Forced failed promotion restores pre-swap bytes | `.next` removed the instant `.old` appeared (watcher t=19.4193 s, `.old` 524288000 B); canary log `SW-24 … rename … no such file or directory (rolled back, sha 7319319216229bcd2f95e23d7fe1d4e0a489ad63cbfb4178c44744eba320c20d size 524288000 verified)`; post-restore canonical 524288000 B, sha256 73193192… = pre-swap; binary runs -> `PROBE v1.18.5`; pid 2247037 still alive, registry `p1204-rb 1.18.5 active` | PROVEN |
| Rollback leaves the host in the pre-update state | **new-version canary pid 2247093 still running and still registered** as `p1204-rb-canary 1.19.1 active` beside `p1204-rb 1.18.5` | **REFUTED** (F4) |
| Windows update | no Windows host: registry `Counter({'linux': 4})`; `which wine wine64` -> none; Windows branch of the swap is exercised only by the fake-FS unit tests | UNPROVEN |
| Windows service install | `probe-agent --service-status` -> `manager=systemd:unavailable`; SCM is required and absent | UNPROVEN |
| Reboot survival | PID 1 = `s6-svscan`; `systemctl: absent`; `/run/systemd` does not exist; no unit installed — the agent ran as a manual child of this session | UNPROVEN |
| Logoff survival | no login session manager on this host (container, no systemd-logind); nothing to log off | UNPROVEN |

## Real update transcript

Pre:

    probe-agent            15216426 B  sha256 2151bde1760a8d83e80b13041f12c400325105f7b4ed19cb96f899635f17def3
    /proc/2246591/exe                                     2151bde1…   (running v1.18.5)
    registry: p1204-agent 1.18.5 active

During (old agent log, pid 2246591):

    22:25:39 [update] received update command: version=1.19.1, file=probe-agent-new, ws_path=""
    22:25:39 [update] downloaded 15343695 bytes
    22:25:39 [update] SHA256 verified: d03769e5c2fc18833a1f80407c282f8ac7fbe7a6b694e38207a7d8deef752856
    22:25:41 [update] new process started: PID=2246668, old PID=2246591
    22:25:50 PROBE Client shutting down...
    22:25:39 canary connected as p1204-agent-canary
    22:25:50 [canary] swap verified: canonical size=15343695 sha=d03769e5c2fc1883,
              backup …/probe-agent.old size=15216426 sha=2151bde1760a8d83
    22:25:50 [canary] update committed — new version in place, old stopped. Becoming canonical agent.

Post:

    run/agent/probe-agent       15343695 B  sha256 d03769e5c2fc18833a1f80407c282f8ac7fbe7a6b694e38207a7d8deef752856
    run/agent/probe-agent.old   15216426 B  sha256 2151bde1760a8d83e80b13041f12c400325105f7b4ed19cb96f899635f17def3
    $ run/agent/probe-agent --version      -> PROBE v1.19.1
    pid 2246668 /proc/exe sha256          -> d03769e5…  (the running image is the new one, not just the file)
    registry                              -> p1204-agent 1.19.1 active, reconnects=2, connection_started_at 22:25:50Z
    health row                            -> version 1.19.1, stale false, last_seen_age 2 s

The swap operated on the real inode of a live process: the canonical path was
replaced while pid 2246668 was running from the staged `.next`, and the parked
`.old` digest equals the pre-swap digest byte for byte.

## Forced-failure rollback (real FS, real process)

Pre-swap canonical padded to widen the hash window: 524288000 B, sha256
7319319216229bcd2f95e23d7fe1d4e0a489ad63cbfb4178c44744eba320c20d.

Inducement: an external watcher removed `<canonical>.next` the moment the park
created `<canonical>.old` (observed t=19.4193 s after arming, `.old` already
524288000 B). The promote rename therefore failed with ENOENT — a real OS error
on a real path, no code edited and no fake filesystem in play.

    [canary] swap failed: SW-24: ROLLBACK_FAILED: rename new into place:
      rename …/probe-agent.next …/probe-agent: no such file or directory
      (rolled back, sha 7319319216229bcd2f95e23d7fe1d4e0a489ad63cbfb4178c44744eba320c20d size 524288000 verified)
      — old keeps running (rollback)

Post-restore, read from disk after the copy:

    run/rb/probe-agent   524288000 B  sha256 7319319216229bcd2f95e23d7fe1d4e0a489ad63cbfb4178c44744eba320c20d
    $ run/rb/probe-agent --version -> PROBE v1.18.5
    pid 2247037 alive; registry p1204-rb 1.18.5 active

Size and sha256 after the restore equal the pre-swap values exactly.

## Findings

F1 (blocking). A stock agent cannot be updated, and the API calls it success.
`capabilityForCommand("agent_update")` requires capability `"update"`
(internal/server/enrollment.go:463-464). Neither default list contains it:
internal/agent/capabilities_elevation.go:18-21 and cmd/probe/config.go:196
(both omit "update"). forwardToAgentWithTimeout refuses before any write
(server_api.go:793-796), and handleAgentUpdate maps every error — including
this local refusal — to HTTP 200 `{"status":"update_in_progress"}`
(server_update.go:260-271). Measured: 0.089 s response (a real send waits up to
30 s), no `[update] received update command` in the agent log, no `.next`, no
registry change, `/api/v1/agents/{id}/audit` -> `{"data":null}`. The update
worked only after adding "update" to the agent's config by hand. This is the
silent false success the programme exists to eliminate.

F2 (high). After a successful commit the promoted process keeps the canary
identity: pid 2246668 argv still `… --canary --canary-old-pid 2246591`, and its
status record says `name=p1204-agent-canary`, `executable=…/probe-agent.next` —
a path that no longer exists (`ls: No such file or directory`). The P12-02
`--service-status` surface therefore reports a binary path and agent name that
contradict the registry. canary.go:70-73 clears `cfg.Name`/`CanaryMode` but the
status record and argv are not re-derived.

F3 (medium). swap.go:299 passes `CodeRollbackFailed` on the branch where the
rollback SUCCEEDED: the log reads `SW-24: ROLLBACK_FAILED: … (rolled back, sha …
verified)`. Code and message contradict each other; an operator grepping for
ROLLBACK_FAILED gets a false alarm, and an automated reader gets a false failure.

F4 (high). There is no exit path on a failed swap (canary.go:56-59): the
new-version canary keeps running, reconnects, and stays registered as a second
active agent — `p1204-rb-canary 1.19.1 active` beside `p1204-rb 1.18.5 active`,
still running from a `.next` that no longer exists. The binary on disk rolled
back; the host did not return to its pre-update process set.

F5 (informational, verified claim). P12-03's "541 pass / 0 fail / 2 skip"
reproduces at this HEAD (541 top-level, 0 fail, 2 skip; 618 incl. subtests).

## Shape

SHAPE reuse internal/upgrade/swap.go:SwapRunningBinary — the only swap
implementation; internal/agent/canary.go:110 swapBinaryPaths is a thin wrapper
(single caller at canary.go:111). No parallel copy of the park/promote logic
exists outside upgrade/swap.go; the remaining os.Rename calls in
agent_update.go:115,200 stage the download and are not a second swap.

## Blocked legs (unproven, with the reason each)

1. Windows update — no Windows node exists in this environment (all four
   registered agents report os=linux; no wine/wine64). The Windows park-then-
   promote branch has only ever run against the fake SwapFS in swap_test.go.
2. Windows service install — requires the Service Control Manager; this host
   answers `manager=systemd:unavailable` and has no SCM.
3. Reboot survival — PID 1 is `s6-svscan`; systemd is absent (`systemctl`
   missing, no `/run/systemd`), no unit was installed, and the agent ran as a
   manual child of this session. There is no init to reboot through and nothing
   registered to be restarted.
4. Logoff survival — same container has no login/session manager
   (systemd-logind absent); there is no interactive session to end.

None of these may be read as passing by omission.

## Open item

F1 must be fixed (advertise "update" in the default capability set, or stop
gating agent_update on a capability no shipped build advertises; and never map a
pre-send refusal to `update_in_progress`) before any milestone claims an
operator can update a stock agent through the API.
