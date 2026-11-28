// @vitest-environment happy-dom
// P27-03 (spec P27-01 §1.1, §2.3, §5.3) — unit tests for the pure view logic.
//
// Each test asserts a BINDING: the value the server puts on the wire is the only
// input, and the rendered claim must follow it. The flips below are the
// acceptance probes for B-3 (encrypted badge toggles with the API value) and for
// the never-green rule; a green chip reachable without its wire evidence would
// fail one of them.
import { describe, expect, it } from 'vitest'
import {
  stateChip, SERVER_STATES, directionChip, uplinkTypeLabel,
  encryptionBadge, payloadLabel, e2ePostureChip,
  carryingVerdict, chainVerdict, rollupChip,
  buildUplinkPayload, canDial, rowIsEditable,
} from './servers-view'
import type { UplinkForm } from './servers-view'
import type { ServerEntry } from '../api/types'

const row = (over: Partial<ServerEntry>): ServerEntry => ({
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

describe('stateChip — the six-value §1.1 enum', () => {
  it('knows exactly the six shipped states, and all six are visually distinct', () => {
    expect(SERVER_STATES.sort()).toEqual(
      ['auth-rejected', 'connected', 'disabled', 'retrying', 'unreachable', 'version-mismatch'],
    )
    // Distinct class per state: two states sharing a class would make the
    // vocabulary invisible in the one surface that is meant to show it.
    const classes = SERVER_STATES.map(s => stateChip(s).className.split(' ').pop())
    expect(new Set(classes).size).toBe(6)
  })

  it('renders connected green and the two terminal states red + terminal', () => {
    expect(stateChip('connected').className).toContain('badge-green')
    for (const s of ['auth-rejected', 'version-mismatch']) {
      expect(stateChip(s).className).toContain('badge-red')
      expect(stateChip(s).terminal).toBe(true)
    }
  })

  it('never maps a terminal state into connected', () => {
    for (const s of ['auth-rejected', 'version-mismatch', 'unreachable']) {
      expect(stateChip(s).className).not.toContain('badge-green')
    }
  })

  it('C3 graceful-unknown: the migrated-away words up/down and any junk are UNKNOWN, never healthy', () => {
    for (const s of ['up', 'down', 'CONNECTED ', 'weird-new-state', '']) {
      const c = stateChip(s)
      // 'CONNECTED ' trims to a known value; everything else must be unknown.
      if (s.trim() === 'connected') continue
      expect(c.known).toBe(false)
      expect(c.className).toContain('badge-gray')
      expect(c.className).not.toContain('badge-green')
    }
  })

  it('an unknown value keeps the raw wire word so the operator can see what arrived', () => {
    expect(stateChip('quarantined').label).toBe('unknown (quarantined)')
    expect(stateChip(null).label).toBe('unknown')
  })
})

describe('directionChip — which side dials', () => {
  it('distinguishes uplink from inbound, and unknown is not a default', () => {
    expect(directionChip('uplink').label).toBe('→ we dial')
    expect(directionChip('inbound').label).toBe('← it dials us')
    expect(directionChip('uplink').className).not.toBe(directionChip('inbound').className)
    expect(directionChip('').known).toBe(false)
    expect(directionChip('sideways').known).toBe(false)
  })
})

describe('uplinkTypeLabel — null is honoured absence, never a fake default', () => {
  it('renders an em dash for a bootstrap row and the window only when scheduled', () => {
    expect(uplinkTypeLabel(null)).toBe('—')
    expect(uplinkTypeLabel('always-on')).toBe('always-on')
    expect(uplinkTypeLabel('manual')).toBe('manual')
    expect(uplinkTypeLabel('scheduled', { start: '08:00', end: '18:00' }, true)).toBe('scheduled 08:00–18:00')
    expect(uplinkTypeLabel('scheduled', { start: '08:00', end: '18:00' }, false)).toContain('outside window')
    expect(uplinkTypeLabel('scheduled', null, true)).toContain('window absent')
  })

  it('an unmapped discipline is quoted, not silently shown as always-on', () => {
    expect(uplinkTypeLabel('sometimes')).toBe('unknown (sometimes)')
  })
})

describe('encryptionBadge — B-3: bound to encrypted.transport, flipped in test', () => {
  it('flips with the API value (the badge is not hardcoded)', () => {
    const tls = encryptionBadge({ transport: 'wss', payload: 'none' })
    const clear = encryptionBadge({ transport: 'ws', payload: 'none' })
    expect(tls.label).toContain('TLS')
    expect(tls.className).toContain('badge-green')
    expect(clear.label).toContain('CLEARTEXT')
    expect(clear.className).toContain('badge-red')
    // The flip is the proof: same component, two wire values, two verdicts.
    expect(tls.className).not.toBe(clear.className)
  })

  it('absent or unmeasured transport renders UNKNOWN — never optimistically green', () => {
    for (const enc of [{ transport: '', payload: '' }, { transport: 'ftp', payload: 'none' }, null, undefined]) {
      const c = encryptionBadge(enc as never)
      expect(c.known).toBe(false)
      expect(c.className).toContain('badge-gray')
      expect(c.className).not.toContain('badge-green')
    }
  })

  it('payload "none" is stated as TLS-only, never as a second green or as E2E', () => {
    expect(payloadLabel({ transport: 'wss', payload: 'none' })).toContain('TLS-only')
    expect(payloadLabel({ transport: 'wss', payload: 'none' })).toContain('not E2E')
    expect(payloadLabel({ transport: 'wss', payload: '' })).toContain('unknown')
  })
})

describe('e2ePostureChip — cipher_error can never read as on', () => {
  it('binds all three postures and surfaces a cipher error as red', () => {
    expect(e2ePostureChip({ active: true, cipher_error: '' }).className).toContain('badge-green')
    expect(e2ePostureChip({ active: false, cipher_error: '' }).className).toContain('badge-gray')
    const err = e2ePostureChip({ active: true, cipher_error: 'no key material' })
    expect(err.className).toContain('badge-red')
    expect(err.label).toContain('CIPHER ERROR')
    expect(e2ePostureChip(null).known).toBe(false)
  })
})

describe('carryingVerdict — §5.3 never-green-while-dead', () => {
  it('rule 1: green ONLY at connected + agents_registered > 0', () => {
    const v = carryingVerdict(row({ state: 'connected', carrying: { channels_open: 2, agents_registered: 3, last_refusal: null } }))
    expect(v.level).toBe('healthy')
    expect(v.chip.className).toContain('badge-green')
  })

  it('rule 3 (the live P25-22 state): connected + 0 registered + a refusal is IMPAIRED, not green', () => {
    const v = carryingVerdict(row({
      state: 'connected',
      carrying: { channels_open: 1, agents_registered: 0, last_refusal: { code: 'FORWARD_REFUSED', reason: 'expected agent info, got type=ping', at: '2026-11-28T10:00:00Z' } },
    }))
    expect(v.level).toBe('impaired')
    expect(v.chip.label).toContain('NOT carrying')
    expect(v.chip.className).not.toContain('badge-green')
    expect(v.detail).toContain('expected agent info')
  })

  it('rule 3b: connected + open-but-unconfirmed channels is also impaired', () => {
    const v = carryingVerdict(row({ state: 'connected', carrying: { channels_open: 2, agents_registered: 0, last_refusal: null } }))
    expect(v.level).toBe('impaired')
    expect(v.chip.className).not.toContain('badge-green')
    expect(v.chip.label).toContain('unconfirmed')
  })

  it('rule 2: connected + nothing attempted is IDLE amber, still not green', () => {
    const v = carryingVerdict(row({ state: 'connected', carrying: { channels_open: 0, agents_registered: 0, last_refusal: null } }))
    expect(v.level).toBe('idle')
    expect(v.chip.className).toContain('badge-yellow')
    expect(v.chip.className).not.toContain('badge-green')
  })

  it('a down link claims nothing about carrying', () => {
    for (const state of ['retrying', 'auth-rejected', 'version-mismatch', 'disabled', 'unreachable', 'up']) {
      const v = carryingVerdict(row({ state, carrying: { channels_open: 0, agents_registered: 0, last_refusal: null } }))
      expect(v.level).toBe('not-connected')
      expect(v.chip.className).not.toContain('badge-green')
    }
  })
})

describe('chainVerdict — §5.3 rule 4: refused registration is never healthy', () => {
  const chain = (over: Partial<Parameters<typeof chainVerdict>[0]>) => ({
    state: 'healthy',
    hops: [{ id: 'server', role: 'server', state: 'up', observed: true, registration_state: 'registered' }],
    failing_hop: undefined,
    degraded_reason: '',
    ...over,
  })

  it('refused folds to red even if the chain object still says healthy', () => {
    const c = chainVerdict(chain({ hops: [{ id: 'server', role: 'server', state: 'down', observed: true, registration_state: 'refused' }] }))
    expect(c.className).toContain('badge-red')
    expect(c.label).toContain('REFUSED')
    expect(c.className).not.toContain('badge-green')
  })

  it('unanswered is amber, not green; healthy+registered is the only green', () => {
    const un = chainVerdict(chain({ hops: [{ id: 'server', role: 'server', state: 'up', observed: true, registration_state: 'unanswered' }] }))
    expect(un.className).toContain('badge-yellow')
    expect(chainVerdict(chain({})).className).toContain('badge-green')
  })

  it('degraded names the failing hop instead of just saying degraded', () => {
    const c = chainVerdict(chain({ state: 'degraded', failing_hop: { id: 'server', error: 'refused' } }))
    expect(c.label).toContain('failing hop: server')
    expect(c.className).not.toContain('badge-green')
  })
})

describe('rollupChip — §5.3 rule 6 at glance level', () => {
  it('green requires connected > 0 AND carrying === connected', () => {
    expect(rollupChip(1, 1, 1).className).toContain('badge-green')
    expect(rollupChip(2, 2, 1).className).toContain('badge-red')
    expect(rollupChip(1, 1, 0).className).toContain('badge-red')
    expect(rollupChip(2, 0, 0).className).toContain('badge-red')
  })

  it('an absent roll-up is unknown, never green', () => {
    const c = rollupChip(undefined, undefined, undefined)
    expect(c.known).toBe(false)
    expect(c.className).not.toContain('badge-green')
    expect(rollupChip(0, 0, 0).known).toBe(true)
  })
})

describe('buildUplinkPayload — B-2: the form posts the documented API shape', () => {
  const form = (over: Partial<UplinkForm> = {}): UplinkForm => ({
    id: 'ovh-edge',
    host: '10.10.10.1',
    port: '7701',
    token: 'tok-secret',
    scheme: 'wss',
    uplinkType: 'manual',
    scheduleStart: '',
    scheduleEnd: '',
    uplink: false,
    ...over,
  })

  it('manual: exactly the §3.2 body, with no schedule keys', () => {
    const { payload, error } = buildUplinkPayload(form())
    expect(error).toBeUndefined()
    expect(payload).toEqual({
      id: 'ovh-edge',
      upstream: 'wss://10.10.10.1:7701',
      token: 'tok-secret',
      enabled: true,
      uplink: false,
      uplink_type: 'manual',
    })
    // schedule_* must be ABSENT (not empty strings) for non-scheduled types.
    expect(payload && 'schedule_start' in payload).toBe(false)
    expect(payload && 'schedule_end' in payload).toBe(false)
  })

  it('scheduled carries the HH:MM pair; always-on omits it', () => {
    const sched = buildUplinkPayload(form({ uplinkType: 'scheduled', scheduleStart: '08:00', scheduleEnd: '18:00' })).payload
    expect(sched?.schedule_start).toBe('08:00')
    expect(sched?.schedule_end).toBe('18:00')
    const always = buildUplinkPayload(form({ uplinkType: 'always-on' })).payload
    expect('schedule_start' in (always || {})).toBe(false)
  })

  it('uplink (forward target) and uplink_type are independent', () => {
    const p = buildUplinkPayload(form({ uplink: true, uplinkType: 'manual' })).payload
    expect(p?.uplink).toBe(true)
    expect(p?.uplink_type).toBe('manual')
  })

  it('refuses locally what the server would refuse: id, host, port, token, window', () => {
    expect(buildUplinkPayload(form({ id: 'bad id' })).error).toContain('id must match')
    expect(buildUplinkPayload(form({ host: '' })).error).toContain('host is required')
    expect(buildUplinkPayload(form({ host: 'ws://10.10.10.1' })).error).toContain('must not include a scheme')
    expect(buildUplinkPayload(form({ port: '0' })).error).toContain('port')
    expect(buildUplinkPayload(form({ port: '70000' })).error).toContain('port')
    expect(buildUplinkPayload(form({ token: '' })).error).toContain('token is required')
    expect(buildUplinkPayload(form({ uplinkType: 'scheduled', scheduleStart: '8:00', scheduleEnd: '18:00' })).error).toContain('HH:MM')
  })

  it('the payload carries no key the server does not read (no parallel payload)', () => {
    const p = buildUplinkPayload(form({ uplinkType: 'scheduled', scheduleStart: '01:00', scheduleEnd: '02:00' })).payload || {}
    const allowed = new Set(['id', 'upstream', 'token', 'enabled', 'uplink', 'uplink_type', 'schedule_start', 'schedule_end'])
    for (const k of Object.keys(p)) expect(allowed.has(k)).toBe(true)
  })
})

describe('row affordances — bootstrap rows are read-only, manual-only dial', () => {
  it('only peers-sourced rows are editable', () => {
    expect(rowIsEditable({ source: 'peers' })).toBe(true)
    expect(rowIsEditable({ source: 'bootstrap' })).toBe(false)
    expect(rowIsEditable({ source: 'local' })).toBe(false)
  })

  it('dial is offered only for an enabled, non-connected manual uplink', () => {
    expect(canDial(row({ uplink_type: 'manual', state: 'disabled', enabled: true }))).toBe(true)
    expect(canDial(row({ uplink_type: 'manual', state: 'unreachable', enabled: true }))).toBe(true)
    expect(canDial(row({ uplink_type: 'manual', state: 'connected', enabled: true }))).toBe(false)
    expect(canDial(row({ uplink_type: 'always-on', state: 'retrying', enabled: true }))).toBe(false)
    expect(canDial(row({ uplink_type: 'manual', state: 'disabled', enabled: false }))).toBe(false)
    expect(canDial(row({ uplink_type: 'manual', state: 'disabled', enabled: true, source: 'bootstrap' }))).toBe(false)
  })
})
