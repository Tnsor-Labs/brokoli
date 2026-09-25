import { useMemo, useState } from 'react'
import { SearchInput } from '@brokoli/ui'
import { CATALOG, FAMILY_COLOR, PALETTE_GROUPS } from '../catalog'
import './panels.css'

export const PALETTE_MIME = 'application/x-brokoli-node'

/** Node library. Drag an item onto the canvas, or click it to add it at the centre of the view. */
export function Palette({ onAdd }: { onAdd: (type: string) => void }) {
  const [query, setQuery] = useState('')
  const groups = useMemo(() => {
    const q = query.trim().toLowerCase()
    return PALETTE_GROUPS.map((group) => ({
      group,
      items: CATALOG.filter((e) => e.group === group && (!q || `${e.label} ${e.description} ${e.type}`.toLowerCase().includes(q))),
    })).filter((g) => g.items.length)
  }, [query])

  return (
    <aside className="ps-palette" aria-label="Node library">
      <header>
        <h3>Node library</h3>
        <p>Drag onto the canvas, or click to add.</p>
      </header>
      <SearchInput value={query} onChange={setQuery} placeholder="Find a node" aria-label="Search nodes" />
      <div className="ps-palette-groups">
        {groups.map(({ group, items }) => (
          <section key={group}>
            <h4>{group}</h4>
            {items.map((item) => (
              <button
                key={item.type}
                type="button"
                draggable
                className="ps-palette-item"
                title={`${item.description}. Click to add, or drag onto the canvas.`}
                aria-label={item.label}
                style={{ ['--family' as string]: FAMILY_COLOR[item.family] }}
                onDragStart={(e) => {
                  e.dataTransfer.setData(PALETTE_MIME, item.type)
                  e.dataTransfer.effectAllowed = 'copy'
                }}
                onClick={() => onAdd(item.type)}
              >
                <span className="ps-palette-badge" aria-hidden="true">
                  <item.glyph size={16} strokeWidth={1.75} />
                </span>
                <span className="ps-palette-text">
                  <strong>{item.label}</strong>
                  <small>{item.description}</small>
                </span>
              </button>
            ))}
          </section>
        ))}
        {!groups.length && <p className="ps-palette-empty">No node matches "{query}".</p>}
      </div>
    </aside>
  )
}
