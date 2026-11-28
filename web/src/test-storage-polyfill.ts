class MemStorage {
  private m = new Map<string, string>()
  getItem(k: string): string | null { return this.m.has(k) ? this.m.get(k)! : null }
  setItem(k: string, v: string): void { this.m.set(String(k), String(v)) }
  removeItem(k: string): void { this.m.delete(k) }
  clear(): void { this.m.clear() }
  key(i: number): string | null { return [...this.m.keys()][i] ?? null }
  get length(): number { return this.m.size }
}

// node >=26 ships an experimental `localStorage` global that is UNDEFINED unless
// --localstorage-file is passed, and it shadows happy-dom's implementation, so
// the DOM env ends up with no storage at all. The production code uses the
// standard browser API; this polyfill lives only in the test environment.
const g = globalThis as Record<string, unknown>
if (typeof g.localStorage === 'undefined' || !g.localStorage) {
  Object.defineProperty(g, 'localStorage', { value: new MemStorage(), configurable: true })
  const w = (globalThis as unknown as { window?: Record<string, unknown> }).window
  if (w && !w.localStorage) {
    Object.defineProperty(w, 'localStorage', { value: g.localStorage, configurable: true })
  }
}

export {}
