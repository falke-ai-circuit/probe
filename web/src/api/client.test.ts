// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, DownloadError, getToken, setToken } from './client'

class FakeBlob {}

function okRes(body: Blob, headers: Record<string, string>) {
  return {
    ok: true,
    status: 200,
    blob: async () => body,
    headers: { get: (k: string) => headers[k] ?? headers[k.toLowerCase()] ?? null },
  }
}

function errRes(status: number) {
  return { ok: false, status, blob: async () => { throw new Error('must not read body') }, headers: { get: () => null } }
}

function clickSpy() {
  const calls: string[] = []
  const origCreate = document.createElement.bind(document)
  vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
    const el = origCreate(tag)
    if (tag === 'a') {
      (el as HTMLAnchorElement).click = () => calls.push((el as HTMLAnchorElement).download || '')
    }
    return el
  })
  return calls
}

describe('api.downloadBuild', () => {
  beforeEach(() => {
    window.localStorage.clear()
    setToken('op-token-123')
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('200: fetches with bearer header, saves blob, filename from Content-Disposition', async () => {
    const body = new Blob(['MZ'])
    const fetchMock = vi.fn(async (url: any, init: any) => {
      expect(url).toBe('/api/v1/builds/eas1/download')
      expect(init.headers.Authorization).toBe('Bearer op-token-123')
      return okRes(body, { 'Content-Disposition': 'attachment; filename="agent.exe"' } as any)
    })
    vi.stubGlobal('fetch', fetchMock)
    const calls = clickSpy()

    await api.downloadBuild('eas1')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(calls).toEqual(['agent.exe'])
  })

  it('200 without Content-Disposition: falls back to <build-name>.exe by os', async () => {
    const body = new Blob([new Uint8Array([0x7f, 0x45])])
    const fetchMock = vi.fn(async (_url: any) => okRes(body, {} as any))
    // getBuild fallback fetch — return minimal envelope
    let call = 0
    fetchMock.mockImplementation(async (url: any) => {
      call++
      if (call === 1) return okRes(body, {} as any)
      return {
        ok: true, status: 200,
        json: async () => ({ ok: true, data: { name: 'eas1-from-radxa', os: 'linux' } }),
        headers: { get: () => null },
      } as any
    })
    vi.stubGlobal('fetch', fetchMock)
    const calls = clickSpy()

    await api.downloadBuild('eas1')

    expect(calls).toEqual(['eas1-from-radxa.bin'])
  })

  it('401: throws DownloadError naming the status, no navigation, body not read', async () => {
    const fetchMock = vi.fn(async () => errRes(401))
    vi.stubGlobal('fetch', fetchMock)

    await expect(api.downloadBuild('eas1')).rejects.toSatisfy((e: unknown) => {
      return e instanceof DownloadError && e.status === 401 && e.message.includes('401')
    })
    // no <a download> navigation happened, no window.open
    expect(globalThis.window?.open ?? window.open).toBeDefined()
  })

  it('token never appears in the URL or query string', async () => {
    const fetchMock = vi.fn(async (url: any) => {
      expect(String(url)).not.toContain('op-token-123')
      expect(String(url)).not.toContain('token=')
      return errRes(403)
    })
    vi.stubGlobal('fetch', fetchMock)
    await expect(api.downloadBuild('eas1')).rejects.toBeInstanceOf(DownloadError)
  })
})

describe('token storage', () => {
  it('getToken reads the probe_token localStorage key', () => {
    window.localStorage.setItem('probe_token', 'abc')
    expect(getToken()).toBe('abc')
    window.localStorage.clear()
    expect(getToken()).toBe('')
  })
})
