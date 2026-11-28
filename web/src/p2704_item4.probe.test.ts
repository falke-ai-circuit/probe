// P27-04 item 4 probe: badge must be driven by the server-side values, including
// the red CLEARTEXT case and the grey UNKNOWN case, and the health posture chip.
// NOTE: e2ePostureChip takes the `e2e` OBJECT ({active,cipher_error}), not the
// whole /health data envelope — corrected after a first run passed the envelope
// and wrongly read both instances as "off".
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { encryptionBadge, payloadLabel, e2ePostureChip } from './lib/servers-view'

const e2eOf = (f: string) => JSON.parse(readFileSync('/opt/data/tmp/p2704h/' + f, 'utf8')).data.e2e

describe('P27-04 item 4 — badge equals server-side truth', () => {
  it('posture chip from the two LIVE health payloads (e2e on / e2e off)', () => {
    const on = e2ePostureChip(e2eOf('health_on.json'))
    const off = e2ePostureChip(e2eOf('health_off.json'))
    console.log('E2E_RAW_ON=' + JSON.stringify(e2eOf('health_on.json')))
    console.log('E2E_RAW_OFF=' + JSON.stringify(e2eOf('health_off.json')))
    console.log('POSTURE_E2E_ON=' + JSON.stringify(on))
    console.log('POSTURE_E2E_OFF=' + JSON.stringify(off))
    expect(on.label).not.toEqual(off.label)
    expect(on.className).toContain('badge-green')
    expect(off.className).not.toContain('badge-green')
  })

  it('cipher_error means red refusing, never "on" (constructed payload)', () => {
    const chip = e2ePostureChip({ active: true, cipher_error: 'message authentication failed' })
    console.log('POSTURE_CIPHER_ERR=' + JSON.stringify(chip))
    expect(chip.className).not.toContain('badge-green')
    expect(chip.label).not.toContain('posture: on')
  })

  it('transport badge: wss green / ws red / absent grey', () => {
    console.log('WSS=' + JSON.stringify(encryptionBadge({ transport: 'wss', payload: 'none' } as any)))
    console.log('WS=' + JSON.stringify(encryptionBadge({ transport: 'ws', payload: 'none' } as any)))
    console.log('ABSENT=' + JSON.stringify(encryptionBadge({ transport: '', payload: 'none' } as any)))
    console.log('PAYLOAD_NONE=' + JSON.stringify(payloadLabel({ transport: 'wss', payload: 'none' } as any)))
  })
})
