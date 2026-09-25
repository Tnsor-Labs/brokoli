import { Search } from 'lucide-react'
import { IconButton, Kbd, cx } from '@brokoli/ui'
import { AlertsBell } from './AlertsBell'
import { CommandPalette } from './CommandPalette'
import { RunActivityButton } from './RunActivity'
import { GlobalKeys, ShortcutHelp } from './Shortcuts'
import type { ShellAction, ShellPage } from './commands'
import { modKey, openOverlay, useOverlay } from './overlay'
import './shell.css'

/** The search entry at the top of the sidebar. */
export function SidebarSearch({ collapsed }: { collapsed: boolean }) {
  if (collapsed)
    return (
      <IconButton label={`Search (${modKey()} K)`} onClick={() => openOverlay('search')} className="sh-search-icon">
        <Search size={16} aria-hidden="true" />
      </IconButton>
    )
  return (
    <button type="button" className="sh-search" onClick={() => openOverlay('search')}>
      <Search size={15} aria-hidden="true" />
      <span>Search</span>
      <Kbd>{modKey()} K</Kbd>
    </button>
  )
}

/**
 * Run activity and alerts, above the account block. `incidents` turns on the
 * incident controls in the alert inbox (assign, acknowledge, resolve); the
 * enterprise app passes it from its capabilities, along with the current
 * user's id so "Assign to me" and "Assigned to you" can be told apart.
 */
export function SidebarStatus({ collapsed, incidents = false, currentUserId }: { collapsed: boolean; incidents?: boolean; currentUserId?: string }) {
  return (
    <div className={cx('sh-status', collapsed && 'is-collapsed')}>
      <RunActivityButton collapsed={collapsed} />
      <AlertsBell collapsed={collapsed} incidents={incidents} currentUserId={currentUserId} />
    </div>
  )
}

const NO_ACTIONS: ShellAction[] = []

/** Keyboard handling and the overlays it opens. Mount once, inside the router. */
export function ShellOverlays({ pages, actions = NO_ACTIONS }: { pages: ShellPage[]; actions?: ShellAction[] }) {
  const overlay = useOverlay()
  const close = () => openOverlay(null)
  return (
    <>
      <GlobalKeys pages={pages} />
      {overlay === 'search' && <CommandPalette pages={pages} actions={actions} onClose={close} />}
      {overlay === 'help' && <ShortcutHelp pages={pages} onClose={close} />}
    </>
  )
}
