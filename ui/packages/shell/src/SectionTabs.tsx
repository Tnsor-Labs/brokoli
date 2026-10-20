import { Link } from 'react-router-dom'
import { useSession } from '@brokoli/auth'
import { cx } from '@brokoli/ui'
import { sectionFor, type Section } from './sections'

/** The tabs of the section the current page belongs to, as links: each tab is its own page. */
export function SectionTabs({ sections, pathname }: { sections: Section[]; pathname: string }) {
  const session = useSession()
  const section = sectionFor(sections, pathname, session.can)
  if (!section) return null
  return (
    <nav className="bk-section-tabs" aria-label={section.label}>
      <div className="bk-section-tabs-in">
        <span className="bk-section-tabs-label">{section.label}</span>
        {section.tabs.map((t) => (
          <Link
            key={t.to}
            to={t.to}
            className={cx('bk-section-tab', t.to === pathname && 'is-active')}
            aria-current={t.to === pathname ? 'page' : undefined}
          >
            {t.label}
          </Link>
        ))}
      </div>
    </nav>
  )
}
