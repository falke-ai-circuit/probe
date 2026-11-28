// P27-04 probe (reviewer lane, NOT product code): render ServersTable from the
// LIVE wire JSON captured off a running probe-server-0cef665, not from fixtures.
import { describe, it, expect } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { createElement as h } from 'react'
import { readFileSync, writeFileSync } from 'node:fs'
import { ServersTable } from './components/ServersTable'

const LIVE = '/opt/data/tmp/p2704g/rows_live.json'
const out: string[] = []

describe('P27-04 live-wire render', () => {
  it('renders the rows the server actually emitted', () => {
    const rows = JSON.parse(readFileSync(LIVE, 'utf8')).data.servers
    const html = renderToStaticMarkup(h(ServersTable, { rows }))
    writeFileSync('/opt/data/tmp/p2704g/rendered_live.html', html)
    out.push('ROWS=' + rows.length)
    out.push('HAS_NOT_CARRYING=' + html.includes('NOT carrying'))
    out.push('HAS_CLEARTEXT=' + html.includes('CLEARTEXT'))
    out.push('HAS_TLS=' + html.includes('TLS'))
    out.push('HAS_UNKNOWN=' + html.includes('ENCRYPTION UNKNOWN'))
    out.push('HAS_IDLE=' + html.includes('idle'))
    out.push('SERVER_MARKS=' + html.split('data-entity="server"').length)
    // every peers row must appear
    for (const r of rows) expect(html).toContain(`data-state="${r.state}"`)
    console.log(out.join('\n'))
    // agent-row side: the real /agents table markup carries no server mark
    const agentsSrc = readFileSync('src/pages/Agents.tsx', 'utf8')
    console.log('AGENTS_PAGE_HAS_ENTITY_MARK=' + /data-entity/.test(agentsSrc))
    console.log('AGENTS_PAGE_HAS_SERVER_CHIP=' + /server-(encrypted|carrying)-badge|server-direction/.test(agentsSrc))
  })
})
