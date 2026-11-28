// @vitest-environment happy-dom
// P27-03 acceptance probes that need a RENDERED DOM, not just the pure logic.
//
// Why renderToStaticMarkup and not a browser screenshot: this host has no
// headless Chrome/Playwright and the disk is at 99 % (6.8 G free) — downloading
// a browser bundle is exactly the install that has already cost this workflow
// two legs. The brief allows a rendered-DOM assertion + component tree instead,
// and says which was used: this file is the DOM half.
//
// B-1 is asserted as a DIFFERENCE: the same markup is produced for a server row
// and for an agent row, and the test proves the server row carries affordances
// the agent row does not (data-entity="server", a server glyph, a direction
// arrow keyed to the API's `direction`). That is the property the scope item
// asks for — a server row must never be mistakable for an agent row.
//
// B-2's wire half: the payload the form builds is pushed through api.createPeer
// with fetch stubbed, and the assertion runs on the BODY THAT WOULD BE SENT.
// That closes the gap between "the builder returns the right object" and "the
// client puts the right object on the wire". The live half (an uplink actually
// connecting) is NOT RUN — no server is reachable from this lane; see artefact.
import { describe, expect, it, vi, afterEach } from 'vitest'
import { createElement as h } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { ServersTable, ChainsTable, DirectionArrow } from './ServersTable'
import { buildUplinkPayload } from '../lib/servers-view'
import type { UplinkForm } from '../lib/servers-view'
import type { ServerEntry, ChainView } from '../api/types'
import { api } from '../api/client'

const row = (over: Partial<ServerEntry> = {}): ServerEntry => ({
  id: 'ovh-edge',
  name: 'ovh-edge',
  role: 'peer',
  listen_addr: '',
  listen_addr_raw: '',
  listen_addr_source: '',
  version: null,
  uptime_seconds: null,
  state: 'connected',
  last_seen: '',
  state_reason: '',
  down_since: null,
  transports: [],
  counts: { direct_agents: 0, relayed_agents: 0 },
  forward_policy: null,
  max_agents: null,
  address: 'wss://10.10.10.1:7701',
  direction: 'uplink',
  uplink_type: 'always-on',
  schedule_window: null,
  in_window: true,
  enabled: true,
  peer_version: null,
  required_min: null,
  encrypted: { transport: 'wss', payload: 'none' },
  carrying: { channels_open: 0, agents_registered: 0, last_refusal: null },
  source: 'peers',
  ...over,
})

// The agent row markup this server row must not be mistakable for: the shape
// /agents renders (StatusBadge inside a plain <tr>, no entity mark, no
// direction, no link-transport chip).
function renderAgentRow(): string {
  return renderToStaticMarkup(
    h('table', null,
      h('tbody', null,
        h('tr', null,
          h('td', null, 'eas1'),
          h('td', null, h('span', { className: 'badge badge-green' }, 'active')),
          h('td', null, '100%'),
        ),
      ),
    ),
  )
}

describe('B-1 — a server row is visually and structurally distinct from an agent row', () => {
  const html = renderToStaticMarkup(h(ServersTable, { rows: [row()] }))
  const agentHtml = renderAgentRow()

  it('the table declares its entity, and so does every row', () => {
    expect(html).toContain('data-testid="servers-table"')
    expect(html).toContain('data-entity="server"')
    expect(agentHtml).not.toContain('data-entity="server"')
  })

  it('the server row carries a server glyph the agent row does not', () => {
    expect(html).toContain('data-testid="server-row-icon"')
    expect(html).toContain('aria-label="server"')
    expect(agentHtml).not.toContain('aria-label="server"')
  })

  it('the direction arrow is present and bound to the API direction value', () => {
    expect(html).toContain('data-testid="server-direction"')
    expect(html).toContain('data-direction="uplink"')
    expect(html).toContain('→ we dial')
    // inbound renders the opposite arrow, not the same chip text
    const inbound = renderToStaticMarkup(h(DirectionArrow, { direction: 'inbound' }))
    expect(inbound).toContain('← it dials us')
    expect(inbound).toContain('data-direction="inbound"')
    expect(agentHtml).not.toContain('data-testid="server-direction"')
  })

  it('the row exposes the link-transport and carrying chips an agent row has no concept of', () => {
    expect(html).toContain('data-testid="server-encrypted-badge"')
    expect(html).toContain('data-testid="server-carrying-badge"')
    expect(agentHtml).not.toContain('server-encrypted-badge')
    expect(agentHtml).not.toContain('server-carrying-badge')
  })

  it('the rendered row carries the wire state as an attribute for diffing', () => {
    expect(html).toContain('data-state="connected"')
    const disabled = renderToStaticMarkup(h(ServersTable, { rows: [row({ state: 'disabled', enabled: false })] }))
    expect(disabled).toContain('data-state="disabled"')
  })
})

describe('B-3 (DOM half) — the encrypted badge text follows the row value', () => {
  it('wss renders TLS, ws renders CLEARTEXT, absent renders UNKNOWN — same component', () => {
    const tls = renderToStaticMarkup(h(ServersTable, { rows: [row({ encrypted: { transport: 'wss', payload: 'none' } })] }))
    const clear = renderToStaticMarkup(h(ServersTable, { rows: [row({ encrypted: { transport: 'ws', payload: 'none' } })] }))
    const unknownRow = renderToStaticMarkup(h(ServersTable, { rows: [row({ encrypted: { transport: '', payload: '' } })] }))
    expect(tls).toContain('TLS · wss')
    expect(clear).toContain('CLEARTEXT · ws')
    expect(unknownRow).toContain('ENCRYPTION UNKNOWN')
    expect(unknownRow).not.toContain('TLS · wss')
  })
})

describe('never-green (DOM half) — the P25-22 state renders red, not healthy', () => {
  it('connected + 0 registered + a refusal renders the impaired chip with the refusal code', () => {
    const html = renderToStaticMarkup(h(ServersTable, {
      rows: [row({
        state: 'connected',
        carrying: {
          channels_open: 1,
          agents_registered: 0,
          last_refusal: { code: 'FORWARD_REFUSED', reason: 'expected agent info, got type=ping', at: '2026-11-28T10:00:00Z' },
        },
      })],
    }))
    expect(html).toContain('NOT carrying')
    expect(html).toContain('state-impaired')
    expect(html).toContain('expected agent info, got type=ping')
    expect(html).toContain('data-testid="server-last-refusal"')
  })

  it('a refused chain registration renders REFUSED even though state says healthy', () => {
    const chains: ChainView[] = [{
      id: 'radxa→ovh',
      state: 'healthy',
      path: ['radxa', 'ovh'],
      hops: [{ id: 'server', role: 'server', state: 'down', observed: true, registration_state: 'refused' }],
      observed_at: '2026-11-28T10:00:00Z',
      observe_bound_ms: 1500,
    }]
    const html = renderToStaticMarkup(h(ChainsTable, { chains }))
    expect(html).toContain('registration REFUSED')
    expect(html).toContain('state-impaired')
    expect(html).toContain('data-testid="chain-registration"')
  })

  it('an unmapped state renders the unknown chip and says so', () => {
    const html = renderToStaticMarkup(h(ServersTable, { rows: [row({ state: 'up' })] }))
    expect(html).toContain('unknown (up)')
    expect(html).toContain('unmapped wire value')
    expect(html).toContain('state-unknown')
  })

  it('bootstrap rows offer no edit affordances', () => {
    const html = renderToStaticMarkup(h(ServersTable, { rows: [row({ source: 'bootstrap', uplink_type: null })] }))
    expect(html).toContain('data-testid="server-readonly"')
    expect(html).not.toContain('data-testid="server-delete"')
    expect(html).toContain('—') // uplink_type null renders the em dash, not "always-on"
  })
})

describe('B-2 (wire half) — the form payload is the body api.createPeer sends', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  const form: UplinkForm = {
    id: 'ovh-edge',
    host: '10.10.10.1',
    port: '7701',
    token: 'tok-secret',
    scheme: 'wss',
    uplinkType: 'manual',
    scheduleStart: '',
    scheduleEnd: '',
    uplink: false,
  }

  it('posts the §3.2 shape to /api/v1/peers — no parallel payload, no extra keys', async () => {
    let seenUrl = ''
    let seenBody: Record<string, unknown> = {}
    let seenMethod = ''
    const fetchMock = vi.fn(async (url: unknown, init: unknown) => {
      seenUrl = String(url)
      seenMethod = String((init as RequestInit).method)
      seenBody = JSON.parse(String((init as RequestInit).body))
      return {
        ok: true,
        status: 201,
        json: async () => ({ ok: true, data: { id: 'ovh-edge', token_fp: 'fp123', token_present: true, enabled: true, uplink_type: 'manual', schedule_start: '', schedule_end: '', listen: '', max_agents: 0, created_at: '', updated_at: '', link_state: 'disabled', state_detail: {} } }),
      } as never
    })
    vi.stubGlobal('fetch', fetchMock)

    const { payload, error } = buildUplinkPayload(form)
    expect(error).toBeUndefined()
    await api.createPeer(payload as Parameters<typeof api.createPeer>[0])

    expect(seenUrl).toBe('/api/v1/peers')
    expect(seenMethod).toBe('POST')
    // The equality the unit asks for: what the form hands the client is what
    // the server receives, field for field.
    expect(seenBody).toEqual(payload)
    expect(seenBody).toEqual({
      id: 'ovh-edge',
      upstream: 'wss://10.10.10.1:7701',
      token: 'tok-secret',
      enabled: true,
      uplink: false,
      uplink_type: 'manual',
    })
  })

  it('scheduled sends the window pair; the read-back carries the same type keys', async () => {
    let seenBody: Record<string, unknown> = {}
    const fetchMock = vi.fn(async (_u: unknown, init: unknown) => {
      seenBody = JSON.parse(String((init as RequestInit).body))
      return {
        ok: true,
        status: 201,
        json: async () => ({ ok: true, data: { id: 'ovh-edge', token_fp: 'fp', token_present: true, enabled: true, uplink_type: 'scheduled', schedule_start: '08:00', schedule_end: '18:00', listen: '', max_agents: 0, created_at: '', updated_at: '', link_state: 'disabled', state_detail: {} } }),
      } as never
    })
    vi.stubGlobal('fetch', fetchMock)

    const { payload } = buildUplinkPayload({ ...form, uplinkType: 'scheduled', scheduleStart: '08:00', scheduleEnd: '18:00' })
    const view = await api.createPeer(payload as Parameters<typeof api.createPeer>[0])
    expect(seenBody.uplink_type).toBe('scheduled')
    expect(seenBody.schedule_start).toBe('08:00')
    expect(seenBody.schedule_end).toBe('18:00')
    expect(view.uplink_type).toBe('scheduled')
  })

  it('the token never appears in the URL', async () => {
    const urls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: unknown, init: unknown) => {
      urls.push(String(url) + JSON.stringify((init as RequestInit).headers))
      return {
        ok: true, status: 201,
        json: async () => ({ ok: true, data: { id: 'x', token_fp: '', token_present: true, enabled: true, listen: '', max_agents: 0, created_at: '', updated_at: '', link_state: '', state_detail: {} } }),
      } as never
    }))
    const { payload } = buildUplinkPayload(form)
    await api.createPeer(payload as Parameters<typeof api.createPeer>[0])
    expect(urls.join('')).not.toContain('tok-secret')
  })
})
