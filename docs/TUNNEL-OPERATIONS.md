# Tunnels — operator notes

What a tunnel create promises, what it refuses, and the network topology it
requires. Read before automating against `POST /api/v1/agents/{id}/tunnel`.

## The create contract

`POST /api/v1/agents/{id}/tunnel {"target_host","target_port","listen_port":0}`
returns `200 {"listen_port":N,"tunnel_id":"tun-M","verified":true}` ONLY after
three proofs completed inside the create call (tunnel.go, since P21-06/P22-07):

1. **Target readiness** — the agent dialed `target_host:target_port` itself
   (`tunnel_probe`) and answered ready.
2. **Loopback** — the server dialed its own listen port and the TCP handshake
   completed.
3. **Data path + first byte** — the server pushed one marker byte through the
   full route (server → agent → target → agent → server) and read it echoed.

Any failed proof answers an HTTP error with NO `listen_port` key and tears the
listener down — a refused port does not answer (dial it and you get
connection-refused, not a hang). Codes: `TUNNEL_BIND_FAILED` (the agent's own
dial error, verbatim), `TUNNEL_READY_TIMEOUT`, `TUNNEL_LOOPBACK_FAILED`,
`TUNNEL_DATA_PATH_FAILED`, `TUNNEL_FIRST_BYTE_FAILED`.

There is no reservation-then-arm mode: the port in a verified response is
already bound and already carrying proof bytes. If you observe
`listen_port` + ConnectionRefused, your server predates this contract — see
"Image skew" below.

## Topology: where the listener lives

The tunnel listener is bound by the **server process**, in the server's
network namespace. Nothing listens on the agent; the agent only connects OUT
to the target when bytes arrive. Consequences for containerized deployments:

- A compose stack publishing only `7701:7701` does NOT expose tunnel
  listeners: a caller outside the server's netns gets connection-refused (or
  a DNAT miss) for every ephemeral tunnel port, even though the create was
  verified — the verification ran from inside the namespace.
- Reaching a verified tunnel from outside is an operator decision with two
  options: run the server with `--network host` (tunnel ports are then host
  ports directly), or publish a port range (compose
  `ports: ["30000-30100:30000-30100"]`) and pass `listen_port` explicitly
  inside that range on every create. `listen_port:0` picks an ephemeral port
  that the stack does not route.

Verify from the server's own namespace first
(`curl -s 127.0.0.1:<port>` inside the container/host running probe-server);
only then reason about routing/publishing.

## Image skew

The proofs above landed across P21-06 (target readiness), P22-07 (first-byte
gate) and peers. The image pinned in this deployment at the time of writing,
`ghcr.io/falke-ai-circuit/probe:70de7fa…` (v1.19.1, tag = commit 70de7fa),
predates them by 120 commits on r119-build: its create publishes
`listen_port` immediately after `net.Listen` with no agent interaction —
exactly the "returns a port, binds a listener but proves nothing, or worse
returns and dies" shape. Any tunnel defect report must name the commit/image
it was reproduced against; v1.19.1 the tag does not mean v1.19.1 the proofs
(commit tags, not feature tags, build these images).

## Known gaps (as of this writing)

- `POST /api/v1/agents/{id}/sniff` builds its session on the same
  `newTunnel` listener but does NOT run the proof chain — it returns
  `listen_port` without verifying the target or the data path. Treat sniff
  ports as unverified; a parity fix is proposed, not yet landed.
- Ephemeral (`listen_port:0`) tunnel ports are chosen by the OS and are not
  part of any published contract — never hardcode them; read them from the
  create response.

Reproduction rig for contract checks lives in the R119 workflow artefacts
(`p1704/`): real server + agent + fake VNC (RFB 003.008) target; create →
read RFB bytes from the published port → targeted refusal case.
