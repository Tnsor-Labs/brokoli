import { useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { Blocks, Bot, CircleHelp, House, KeyRound, LayoutDashboard, PanelLeftClose, PanelLeftOpen, Settings, Waypoints, Workflow, type LucideIcon } from 'lucide-react'
import { SectionTabs, ShellOverlays, SidebarSearch, SidebarStatus, itemActive, type Section, type ShellPage } from '@brokoli/shell'
import { SidebarAccount } from '@brokoli/auth'
import { AppShell, Brand, BrandIcon, IconButton, Menu, NavSection, cx, type BrandIconName } from '@brokoli/ui'
import { useNeedsYou } from './home/needsYou'
import './shell.css'

// The icon is a Lucide component, except Connections, which keeps its brand-sprite glyph.
type Item = { to: string; label: string; icon: LucideIcon | BrandIconName; match: string[]; count?: boolean }

/*
 * Community navigation: eight places, grouped by what a person is doing,
 * the same shape as Enterprise's. Pages that belong together share one item
 * and a tab strip (SECTIONS below), each keeping its own address; Settings
 * and Help sit at the bottom. Search finds every page by its own name.
 */
const NAV: { label?: string; items: Item[] }[] = [
  {
    items: [
      { to: '/home', label: 'Home', icon: House, match: ['/home'], count: true },
      { to: '/pipelines', label: 'Pipelines', icon: Workflow, match: ['/pipelines'] },
      { to: '/agents', label: 'Agents', icon: Bot, match: ['/agents'] },
    ],
  },
  {
    label: 'Observe',
    items: [
      { to: '/dashboard', label: 'Dashboard', icon: LayoutDashboard, match: ['/dashboard', '/calendar'] },
      { to: '/lineage', label: 'Lineage', icon: Waypoints, match: ['/lineage', '/dependencies'] },
    ],
  },
  {
    label: 'Connect',
    items: [
      { to: '/connections', label: 'Connections', icon: 'bk-connections', match: ['/connections'] },
      { to: '/variables', label: 'Secrets & variables', icon: KeyRound, match: ['/variables', '/secret-stores'] },
      { to: '/plugins', label: 'Integrations', icon: Blocks, match: ['/plugins', '/drivers'] },
    ],
  },
]

/** Pages that share an item, as tabs. */
const SECTIONS: Section[] = [
  { label: 'Dashboard', tabs: [{ to: '/dashboard', label: 'Overview' }, { to: '/calendar', label: 'Schedule' }] },
  { label: 'Lineage', tabs: [{ to: '/lineage', label: 'Datasets' }, { to: '/dependencies', label: 'Pipeline dependencies' }] },
  { label: 'Secrets & variables', tabs: [{ to: '/variables', label: 'Variables' }, { to: '/secret-stores', label: 'Secret stores' }] },
  { label: 'Integrations', tabs: [{ to: '/plugins', label: 'Plugins' }, { to: '/drivers', label: 'Drivers' }] },
]

/** Pages offered by search and the "g" shortcuts, each by its own name, with the letters they always had. */
const PAGES: ShellPage[] = [
  { to: '/home', label: 'Home', group: 'Home', key: 'h' },
  { to: '/pipelines', label: 'Pipelines', group: 'Pipelines', key: 'p' },
  { to: '/agents', label: 'Agents', group: 'Agents', key: 'e' },
  { to: '/dashboard', label: 'Dashboard', group: 'Observe', key: 'd' },
  { to: '/calendar', label: 'Schedule (calendar)', group: 'Observe', key: 'm' },
  { to: '/lineage', label: 'Lineage', group: 'Observe', key: 'l' },
  { to: '/dependencies', label: 'Pipeline dependencies', group: 'Observe', key: 'n' },
  { to: '/connections', label: 'Connections', group: 'Connect', key: 'c' },
  { to: '/variables', label: 'Variables', group: 'Connect', key: 'v' },
  { to: '/secret-stores', label: 'Secret stores', group: 'Connect' },
  { to: '/plugins', label: 'Plugins', group: 'Connect' },
  { to: '/drivers', label: 'Drivers', group: 'Connect' },
  { to: '/settings', label: 'Settings', group: 'Workspace', key: 's' },
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

function NavItem({ item, collapsed, pathname, count }: { item: Item; collapsed: boolean; pathname: string; count: number }) {
  const active = itemActive(item.match, pathname)
  return (
    <NavLink
      to={item.to}
      title={collapsed ? (count > 0 ? `${item.label}, ${count} need you` : item.label) : undefined}
      aria-current={active ? 'page' : undefined}
      className={() => cx('bk-nav-item', active && 'active')}
    >
      {typeof item.icon === 'string' ? <BrandIcon name={item.icon} size={18} /> : <item.icon size={18} aria-hidden="true" />}
      {!collapsed && <span className="cm-nav-label">{item.label}</span>}
      {count > 0 &&
        (collapsed ? (
          <span className="cm-nav-dot" aria-hidden="true" />
        ) : (
          <span className="cm-nav-count" aria-label={`${count} need you`}>
            {count > 99 ? '99+' : count}
          </span>
        ))}
    </NavLink>
  )
}

function HelpMenu({ collapsed }: { collapsed: boolean }) {
  const navigate = useNavigate()
  return (
    <Menu
      label="Help"
      align="start"
      items={[
        { id: 'api', label: 'API and CLI reference', onSelect: () => navigate('/api') },
        { id: 'docs', label: 'Documentation', hint: 'on GitHub', onSelect: () => window.open('https://github.com/Tnsor-Labs/brokoli/tree/main/docs', '_blank', 'noopener') },
      ]}
      trigger={(props) => (
        <button type="button" {...props} className={cx('bk-nav-item', 'cm-help-trigger')} title={collapsed ? 'Help' : undefined}>
          <CircleHelp size={18} aria-hidden="true" />
          {!collapsed && <span className="cm-nav-label">Help</span>}
        </button>
      )}
    />
  )
}

export function Shell() {
  const [stored, setCollapsed] = useState(readCollapsed)
  const { pathname } = useLocation()
  const needs = useNeedsYou()
  // The editor needs the width for its canvas; the sidebar folds to a rail there without changing the saved preference.
  const inEditor = /\/edit$/.test(pathname)
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
  const count = needs.ready ? needs.total : 0
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
            {NAV.map((section, i) => (
              <NavSection key={section.label ?? i} label={collapsed ? undefined : section.label}>
                {section.items.map((item) => (
                  <NavItem key={item.to} item={item} collapsed={collapsed} pathname={pathname} count={item.count ? count : 0} />
                ))}
              </NavSection>
            ))}
          </div>
          <div className="cm-sidebar-bottom">
            <NavItem item={{ to: '/settings', label: 'Settings', icon: Settings, match: ['/settings', '/api'] }} collapsed={collapsed} pathname={pathname} count={0} />
            <HelpMenu collapsed={collapsed} />
          </div>
          <SidebarStatus collapsed={collapsed} />
          <SidebarAccount collapsed={collapsed} />
        </>
      }
    >
      <SectionTabs sections={SECTIONS} pathname={pathname} />
      <Outlet />
      <ShellOverlays pages={PAGES} />
    </AppShell>
  )
}
