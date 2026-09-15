import { useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { Kbd, Modal } from '@brokoli/ui'
import { goTarget, isTypingTarget, type ShellPage } from './commands'
import { currentOverlay, modKey, openOverlay } from './overlay'

const G_WINDOW_MS = 1500

/*
 * Global keys. Cmd or Ctrl and K toggles search from anywhere, including
 * inside a field. The other keys (slash, ?, and "g" followed by a letter)
 * only act when the user is not typing and no dialog is open, so they never
 * steal a character from a form or the code editor. The previous handler
 * ignored Cmd/Ctrl+Shift+K, fired inside contentEditable editors, and added
 * a new listener on every "g" press.
 */
export function GlobalKeys({ pages }: { pages: ShellPage[] }) {
  const navigate = useNavigate()
  const pagesRef = useRef(pages)
  pagesRef.current = pages
  useEffect(() => {
    let gAt = 0
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing) return
      const mod = e.metaKey || e.ctrlKey
      const dialog = document.querySelector('[aria-modal="true"]')
      if (mod && !e.altKey && e.key.toLowerCase() === 'k') {
        // A different dialog owns the keyboard; do not stack the palette on top of it.
        if (dialog && !dialog.classList.contains('sh-palette')) return
        e.preventDefault()
        openOverlay(currentOverlay() === 'search' ? null : 'search')
        return
      }
      if (mod || e.altKey || dialog || isTypingTarget(e.target)) return
      if (e.key === '/') {
        e.preventDefault()
        openOverlay('search')
      } else if (e.key === '?') {
        e.preventDefault()
        openOverlay('help')
      } else if (gAt && Date.now() - gAt < G_WINDOW_MS) {
        gAt = 0
        const target = goTarget(e.key, pagesRef.current)
        if (target) {
          e.preventDefault()
          navigate(target.to)
        }
      } else if (e.key === 'g' && !e.shiftKey) {
        gAt = Date.now()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navigate])
  return null
}

export function ShortcutHelp({ pages, onClose }: { pages: ShellPage[]; onClose: () => void }) {
  const go = pages.filter((p) => p.key)
  return (
    <Modal title="Keyboard shortcuts" description="Letter keys work when you are not typing in a field." onClose={onClose}>
      <div className="sh-keys">
        <section>
          <h3>Anywhere</h3>
          <dl>
            <dt>
              <Kbd>{modKey()}</Kbd> <Kbd>K</Kbd>
            </dt>
            <dd>Search pages, pipelines, connections and variables</dd>
            <dt>
              <Kbd>/</Kbd>
            </dt>
            <dd>Search</dd>
            <dt>
              <Kbd>?</Kbd>
            </dt>
            <dd>Show this list</dd>
            <dt>
              <Kbd>Esc</Kbd>
            </dt>
            <dd>Close the open dialog or panel</dd>
          </dl>
        </section>
        <section>
          <h3>Go to a page</h3>
          <dl>
            {go.map((p) => (
              <div key={p.to} className="sh-keys-row">
                <dt>
                  <Kbd>G</Kbd> then <Kbd>{p.key!.toUpperCase()}</Kbd>
                </dt>
                <dd>{p.label}</dd>
              </div>
            ))}
          </dl>
        </section>
      </div>
    </Modal>
  )
}
