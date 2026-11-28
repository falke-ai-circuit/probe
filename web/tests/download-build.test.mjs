// P25-14: unit test for the authenticated artefact download helper
// (api.downloadBuild in web/src/api/client.ts).
//
// web/package.json has no test runner; this drives the helper with esbuild
// (already in node_modules via vite) plus stubbed browser globals.
// Brief acceptance:
//   200 + Content-Disposition -> blob saved, filename from the header
//   200 without header        -> fallback filename per build OS
//   401                       -> typed DownloadError, no navigation opened
//
// Run:  node tests/download-build.test.mjs   (exit 0 = all cases pass)
import { build } from 'esbuild'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'

const tmp = mkdtempSync(join(tmpdir(), 'p2514-'))

// Bundle client.ts to CJS so a plain node require() can load it with stubs.
await build({
  entryPoints: ['src/api/client.ts'],
  bundle: true,
  format: 'cjs',
  platform: 'node',
  outfile: join(tmp, 'client.cjs'),
  logLevel: 'silent',
})

// Runner: assigns stubs BEFORE requiring the module, exports stub state so
// assertions can inspect what the helper did (anchor clicked, download name).
writeFileSync(join(tmp, 'run.cjs'), `
const store = new Map()
let lastAnchor = null
let navAttempts = 0
const stubURL = {
  createObjectURL: () => 'blob:p2514-stub',
  revokeObjectURL: (u) => { store.set('__revoked__', u) },
}
Object.assign(globalThis, {
  localStorage: {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, v),
    removeItem: (k) => store.delete(k),
  },
  document: {
    body: { appendChild: () => {}, removeChild: () => {} },
    createElement: (tag) => {
      const el = { tag, href: '', download: '', clicked: false,
        click() { this.clicked = true }, remove: () => {} }
      lastAnchor = el
      return el
    },
  },
  URL: stubURL,
  window: { open: () => { navAttempts++ ; return null } },
})
module.exports = { api: require('./client.cjs').api, DownloadError: require('./client.cjs').DownloadError,
  getState: () => ({ lastAnchor, navAttempts, store }),
  setToken: (t) => store.set('probe_token', t),
  resetState: () => { lastAnchor = null; navAttempts = 0 } }
`)

const mod = await import(pathToFileURL(join(tmp, 'run.cjs')).href)
const { api, DownloadError, getState, setToken, resetState } = mod.default ?? mod

let failures = 0
function check(name, cond, detail) {
  if (cond) console.log(`PASS ${name}`)
  else { failures++; console.log(`FAIL ${name}${detail ? ' — ' + detail : ''}`) }
}

// Case 1: 200 + Content-Disposition -> filename from header, anchor clicked
// with an object URL, no navigation attempt.
{
  setToken('tok-abc')
  const calls = []
  globalThis.fetch = async (url, init) => {
    calls.push({ url, auth: init?.headers?.Authorization })
    return {
      ok: true, status: 200,
      headers: { get: (h) => (h.toLowerCase() === 'content-disposition'
        ? 'attachment; filename="probe-agent.exe"' : null) },
      blob: async () => ({ size: 12185088, marker: 'mz' }),
    }
  }
  let threw = null
  try { await api.downloadBuild('b-1') } catch (e) { threw = e }
  const s = getState()
  check('C1 200-header: no throw', threw === null, threw && String(threw))
  check('C1 200-header: bearer header sent', calls[0]?.auth === 'Bearer tok-abc',
    JSON.stringify(calls[0]))
  check('C1 200-header: URL has no token', !String(calls[0]?.url).includes('tok-abc'))
  check('C1 200-header: filename from CD', s.lastAnchor?.download === 'probe-agent.exe',
    s.lastAnchor?.download)
  check('C1 200-header: anchor clicked', s.lastAnchor?.clicked === true)
  check('C1 200-header: objectURL revoked', s.store.get('__revoked__') === 'blob:p2514-stub')
  check('C1 200-header: no navigation', s.navAttempts === 0)
  globalThis.fetch = undefined
}

// Case 2: 200 without Content-Disposition -> fallback name by build OS.
{
  setToken('tok-abc')
  globalThis.fetch = async (url, init) => ({
    ok: true, status: 200,
    headers: { get: () => {
      // getBuild(id) uses apiFetch -> fetch with same stub; resolve build cfg
      // by URL.
      return null } },
    blob: async () => ({ size: 1 }),
  })
  // getBuild rides the same fetch stub but goes through res.json() — give it
  // a json() only when the URL is the config endpoint.
  globalThis.fetch = async (url, init) => {
    if (String(url).endsWith('/builds/b-2')) {
      return { ok: true, status: 200, headers: { get: () => null },
        json: async () => ({ ok: true, data: { name: 'radxa-build', os: 'linux' } }) }
    }
    return { ok: true, status: 200, headers: { get: () => null },
      blob: async () => ({ size: 1, marker: 'elf' }) }
  }
  let threw = null
  try { await api.downloadBuild('b-2') } catch (e) { threw = e }
  const s = getState()
  check('C2 fallback: no throw', threw === null, threw && String(threw))
  check('C2 fallback: .bin for linux os', s.lastAnchor?.download === 'radxa-build.bin',
    s.lastAnchor?.download)
  globalThis.fetch = undefined
}

// Case 3: 401 -> DownloadError with status, no anchor click, no navigation.
{
  resetState()
  setToken('tok-abc')
  globalThis.fetch = async (url, init) => ({
    ok: false, status: 401,
    headers: { get: () => null },
    blob: async () => { throw new Error('must not read body on 401') },
  })
  let threw = null
  try { await api.downloadBuild('b-3') } catch (e) { threw = e }
  const s = getState()
  // State-leak guard: reset the harness anchor memory before C3 so a non-click
  // is distinguishable from case 2's anchor still hanging around.
  check('C3 401: throws DownloadError', threw instanceof DownloadError, String(threw))
  check('C3 401: status 401 on error', threw?.status === 401)
  check('C3 401: no anchor click', s.lastAnchor === null || s.lastAnchor.clicked === false,
    JSON.stringify(s.lastAnchor))
  check('C3 401: no navigation', s.navAttempts === 0)
  globalThis.fetch = undefined
}

rmSync(tmp, { recursive: true, force: true })
console.log(failures === 0 ? 'ALL PASS' : `${failures} FAILURE(S)`)
process.exit(failures === 0 ? 0 : 1)
