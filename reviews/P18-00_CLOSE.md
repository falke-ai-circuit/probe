# P18-00 — close record (frontend visual audit)

unit: P18-00
artefact: /opt/data/tmp/probe119/P18-00_VISUAL_AUDIT.md
mirror: reviews/P18-00_VISUAL_AUDIT.md (this repo) + reviews/P18-00-shots/ (17 live captures)
reviewed_by: reviewer (independent; authored none of this UI)
verdict: FIX-FIRST
tree: read-only — no source file, no build, no config and no live agent touched. Redeploy/build POSTs were intercepted and fulfilled locally; zero mutations on 139.99.209.235:7701.
scope: 11 routed pages + Login, 10 nav items, duplication table (8 rows), 9 dead/orphan rows, IA proposal with numbered removals and the honest cost of each.
open item: the one removal with a behavioural cost is the Dashboard agent table — confirm with the owner before P18-01 builds it.
