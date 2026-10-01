import { useMemo, useState } from 'react'
import { SearchInput } from '@brokoli/ui'
import { FAMILY_COLOR, paletteGroups } from '../catalog'
import './panels.css'

export const PALETTE_MIME = 'application/x-brokoli-node'

/**
 * Node library. Drag an item onto the canvas, or click it to add it at the centre of the view.
 * `hidden` holds the node types this server refuses; they are not offered.
 */
export function Palette({ onAdd, hidden }: { onAdd: (type: string) => void; hidden?: ReadonlySet<string> }) {
  const [query, setQuery] = useState('')
  const groups = useMemo(() => paletteGroups(query, hidden), [query, hidden])

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
