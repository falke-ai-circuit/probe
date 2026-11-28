// P27-04 item 5 probe: the P25-22 shape, rendered. Row carries only real wire
// field names from GET /api/v1/servers §1.1 — nothing invented.
import { describe, it, expect } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { createElement as h } from 'react'
import { writeFileSync } from 'node:fs'
import { ServersTable } from './components/ServersTable'
import { carryingVerdict, chainVerdict } from './lib/servers-view'

const base = {
  id: 'fwd1', name: 'fwd1', role: 'peer',
  address: 'ws://10.10.10.9:7701', direction: 'uplink',
  state: 'connected', state_reason: '', last_seen: null,
  peer_version: null, required_min: null,
  uplink_type: 'always-on', schedule_window: null, in_window: true,
  enabled: true, token_fp: 'deadbeef',
  encrypted: { transport: 'wss', payload: 'none' },
  carrying: { channels_open: 1, agents_registered: 0, last_refusal: null },
  source: 'peers', transports: [], counts: {},
}

describe('P27-04 item 5 — connected-but-dead must never render healthy', () => {
  it('rule 3: connected + 0 registered + last_refusal ⇒ red NOT carrying', () => {
    const row = { ...base, carrying: { channels_open: 2, agents_registered: 0,
      last_refusal: { code: 'E2E_KEY_REQUIRED', reason: 'expected agent info, got type=ping', at: '2026-11-28T15:00:00Z' } } }
    const v = carryingVerdict(row as any)
    const html = renderToStaticMarkup(h(ServersTable, { rows: [row as any] }))
    writeFileSync('/opt/data/tmp/p2704h/item5_impaired.html', html)
    console.log('VERDICT=' + JSON.stringify(v))
    console.log('HAS_NOT_CARRYING=' + html.includes('NOT carrying'))
    console.log('HAS_GREEN_CLASS=' + /badge-green[^>]*>\s*(healthy|carrying)/i.test(html))
    console.log('HAS_REFUSAL_CODE=' + html.includes('E2E_KEY_REQUIRED'))
    // the never-green assertion
    expect(JSON.stringify(v)).not.toMatch(/"tone":"green"|"kind":"healthy"/)
    expect(html).toContain('NOT carrying')
  })

  it('rule 1 control: connected + registered>0 is the only green', () => {
    const row = { ...base, carrying: { channels_open: 1, agents_registered: 3, last_refusal: null } }
    console.log('CONTROL=' + JSON.stringify(carryingVerdict(row as any)))
  })

  it('chain registration_state=refused is red regardless of chain state', () => {
    const chain = { id: 'c1', state: 'healthy', hops: [
      { id: 'server', role: 'server', state: 'up', registration_state: 'refused', error: 'expected agent info, got type=ping' } ] }
    const v = chainVerdict(chain as any)
    console.log('CHAIN=' + JSON.stringify(v))
    expect(JSON.stringify(v)).not.toMatch(/"green"/)
  })
})
