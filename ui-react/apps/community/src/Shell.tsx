import { useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { Braces, CalendarDays, LayoutDashboard, Network, PanelLeftClose, PanelLeftOpen, Plug, Settings, Waypoints, Workflow, type LucideIcon } from 'lucide-react'
import { ShellOverlays, SidebarSearch, SidebarStatus, type ShellPage } from '@brokoli/shell'
import { SidebarAccount } from '@brokoli/auth'
import { AppShell, Brand, BrandIcon, IconButton, NavSection, cx, type BrandIconName } from '@brokoli/ui'
import './shell.css'

// The icon is a Lucide component, except Connections, which keeps its current brand-sprite glyph.
type Item = { to: string; label: string; icon: LucideIcon | BrandIconName; ready?: boolean; key?: string }

/*
 * Community navigation. Enterprise has its own shell and its own list, so
 * nothing here needs to know about organisation or platform pages. Items
 * whose pages are not migrated yet stay visible, marked, so the product
 * does not appear to have lost features.
 */
const SECTIONS: { label: string; items: Item[] }[] = [
  {
    label: 'Build',
    items: [
      { to: '/pipelines', label: 'Pipelines', icon: Workflow, ready: true, key: 'p' },
      { to: '/connections', label: 'Connections', icon: 'bk-connections', ready: true, key: 'c' },
      { to: '/variables', label: 'Variables', icon: Braces, ready: true, key: 'v' },
      { to: '/plugins', label: 'Plugins', icon: Plug, ready: true },
    ],
  },
  {
    label: 'Observe',
    items: [
      { to: '/dashboard', label: 'Dashboard', icon: LayoutDashboard, ready: true, key: 'd' },
      { to: '/calendar', label: 'Calendar', icon: CalendarDays, ready: true, key: 'm' },
      { to: '/lineage', label: 'Lineage', icon: Waypoints, ready: true, key: 'l' },
      { to: '/dependencies', label: 'Dependencies', icon: Network, ready: true, key: 'n' },
    ],
  },
  {
    label: 'Workspace',
    items: [{ to: '/settings', label: 'Settings', icon: Settings, ready: true, key: 's' }],
  },
]

/** Pages offered by search and the "g" shortcuts: only those that exist in this interface. */
const PAGES: ShellPage[] = [
  ...SECTIONS.flatMap((s) => s.items.filter((i) => i.ready).map((i) => ({ to: i.to, label: i.label, group: s.label, key: i.key }))),
  { to: '/api', label: 'API and CLI reference', group: 'Help', key: 'a' },
]

const SIDEBAR_KEY = 'brokoli-sidebar'

function readCollapsed() {
  try {
    return Boolean((JSON.parse(localStorage.getItem(SIDEBAR_KEY) ?? '{}') as { collapsed?: boolean }).collapsed)
  } catch {
    return false
  }
}

export function Shell() {
  const [stored, setCollapsed] = useState(readCollapsed)
  // The editor needs the width for its canvas; the sidebar folds to a rail there without changing the saved preference.
  const inEditor = /\/edit$/.test(useLocation().pathname)
  const collapsed = stored || inEditor
  const toggle = () => {
    const next = !stored
    setCollapsed(next)
    try {
      localStorage.setItem(SIDEBAR_KEY, JSON.stringify({ collapsed: next }))
    } catch {
      /* Collapse state is a convenience; losing it is harmless. */
    }
  }
  return (
    <AppShell
      collapsed={collapsed}
      sidebar={
        <>
          <div className="cm-sidebar-head">
            <Brand edition={collapsed ? undefined : 'Community'} compact={collapsed} />
            <IconButton size="sm" label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} onClick={toggle} className="cm-collapse">
              {collapsed ? <PanelLeftOpen size={16} aria-hidden="true" /> : <PanelLeftClose size={16} aria-hidden="true" />}
            </IconButton>
          </div>
          <div className="cm-sidebar-nav">
            <SidebarSearch collapsed={collapsed} />
            {SECTIONS.map((section) => (
              <NavSection key={section.label} label={collapsed ? undefined : section.label}>
                {section.items.map((item) => (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    title={collapsed ? item.label : item.ready ? undefined : `${item.label} is not migrated yet`}
                    className={({ isActive }) => cx('bk-nav-item', isActive && 'active', !item.ready && 'is-pending')}
                  >
                    {typeof item.icon === 'string' ? <BrandIcon name={item.icon} size={18} /> : <item.icon size={18} aria-hidden="true" />}
                    {!collapsed && <span className="cm-nav-label">{item.label}</span>}
                    {!collapsed && !item.ready && <span className="cm-nav-soon">Soon</span>}
                  </NavLink>
                ))}
              </NavSection>
            ))}
          </div>
          <SidebarStatus collapsed={collapsed} />
          <SidebarAccount collapsed={collapsed} />
        </>
      }
    >
      <Outlet />
      <ShellOverlays pages={PAGES} />
    </AppShell>
  )
}
