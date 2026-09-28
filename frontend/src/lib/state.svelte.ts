import { EventsOn } from '../../wailsjs/runtime/runtime'
import { Overview, Accounts } from '../../wailsjs/go/main/App'
import type { main } from '../../wailsjs/go/models'
import { err } from './fmt'

export type View = 'overview' | 'games' | 'mods' | 'accounts' | 'devices' | 'backup' | 'settings'

type Toast = { id: number; text: string; kind: 'info' | 'ok' | 'err' }

export const ui = $state({
  view: 'overview' as View,
  overview: null as main.Overview | null,
  accounts: null as main.AccountsView | null, // loaded while accounts are on
  toasts: [] as Toast[],
  tick: 0, // bumps on every backend "changed" event so views can refetch
  gamesAdded: 0, // bumps when games were added automatically
})

let seq = 0
export function toast(text: string, kind: Toast['kind'] = 'info') {
  const id = ++seq
  ui.toasts.push({ id, text, kind })
  setTimeout(() => { ui.toasts = ui.toasts.filter(t => t.id !== id) }, kind === 'err' ? 7000 : 3500)
}

export function fail(e: unknown) { toast(err(e), 'err') }

/** Run an action, toast its error, and return whether it succeeded. */
export async function attempt<T>(fn: () => Promise<T>, okText?: string): Promise<boolean> {
  try {
    await fn()
    if (okText) toast(okText, 'ok')
    return true
  } catch (e) {
    fail(e)
    return false
  }
}

export async function refresh() {
  try { ui.overview = await Overview() } catch { /* backend not ready yet */ }
  await refreshAccounts()
}

export async function refreshAccounts() {
  if (!ui.overview?.settings.accounts && !ui.accounts?.splits?.length) { ui.accounts = null; return }
  try { ui.accounts = await Accounts() } catch { /* backend not ready yet */ }
}

/** An account's display name ("" when unknown). */
export function accountName(id: string | undefined): string {
  return ui.accounts?.accounts?.find(a => a.id === id)?.name ?? ''
}

/** An account's color, or a neutral one. */
export function accountColor(id: string | undefined): string {
  return ui.accounts?.accounts?.find(a => a.id === id)?.color || 'var(--muted)'
}

export function applyTheme(t: string) {
  const root = document.documentElement
  if (t === 'light' || t === 'dark') root.dataset.theme = t
  else delete root.dataset.theme
}

let pending: ReturnType<typeof setTimeout> | null = null

export function init() {
  refresh()
  EventsOn('changed', () => {
    if (pending) return
    pending = setTimeout(() => { pending = null; ui.tick++; refresh() }, 400)
  })
  EventsOn('toast', (t: string) => toast(t, 'ok'))
  EventsOn('toast:error', (t: string) => toast(t, 'err'))
  EventsOn('games:added', () => { ui.gamesAdded++ })
  setInterval(refresh, 15000)
}
