import { Suspense, lazy, useEffect, useState } from 'react'
import type { Extension } from '@codemirror/state'
import { Button, ConfirmDialog, Kbd, Modal, Spinner, useTheme } from '@brokoli/ui'

const CodeMirror = lazy(() => import('@uiw/react-codemirror'))

export type CodeLanguage = 'python' | 'sql' | 'typescript' | 'yaml' | 'json'

async function languageExtension(language: CodeLanguage): Promise<Extension> {
  switch (language) {
    case 'python':
      return (await import('@codemirror/lang-python')).python()
    case 'sql':
      return (await import('@codemirror/lang-sql')).sql()
    case 'yaml':
      return (await import('@codemirror/lang-yaml')).yaml()
    default:
      return (await import('@codemirror/lang-javascript')).javascript({ typescript: language === 'typescript' })
  }
}

const HINTS: Record<CodeLanguage, string> = {
  python:
    'The script receives columns, rows, config and params, and sets output_data = {"columns": [...], "rows": [...]}. Lines printed to stderr become run warnings.',
  typescript: 'TypeScript runs in the server code worker pool and needs Node 20 or newer on the server.',
  sql: 'Runs on the connection chosen for this node. ${...} placeholders (variables, run parameters, ${interval.start}) are substituted before the query is sent.',
  yaml: '',
  json: '',
}

/*
 * Full-screen code editor (CodeMirror 6, loaded only when opened). Edits are
 * local until Save; closing with unsaved text asks first.
 */
export function CodeEditorModal({
  title,
  language,
  value,
  readonly = false,
  onSave,
  onClose,
}: {
  title: string
  language: CodeLanguage
  value: string
  readonly?: boolean
  onSave: (text: string) => void
  onClose: () => void
}) {
  const { theme } = useTheme()
  const [text, setText] = useState(value)
  const [extension, setExtension] = useState<Extension | null>(null)
  const [confirm, setConfirm] = useState(false)
  const changed = text !== value

  useEffect(() => {
    let active = true
    void languageExtension(language).then((ext) => active && setExtension(ext))
    return () => {
      active = false
    }
  }, [language])

  const close = () => (changed && !readonly ? setConfirm(true) : onClose())

  return (
    <>
      <Modal
        title={title}
        description={HINTS[language] || undefined}
        size="xl"
        onClose={close}
        className="ps-code-modal"
        footer={
          <>
            <span className="ps-code-keys">
              <Kbd>Ctrl</Kbd> <Kbd>S</Kbd> save · <Kbd>Esc</Kbd> close
            </span>
            <Button variant="ghost" onClick={close}>
              {readonly ? 'Close' : 'Cancel'}
            </Button>
            {!readonly && (
              <Button variant="primary" onClick={() => onSave(text)} disabled={!changed}>
                Save
              </Button>
            )}
          </>
        }
      >
        <div
          className="ps-code-editor"
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
              e.preventDefault()
              e.stopPropagation()
              if (!readonly && changed) onSave(text)
            }
          }}
        >
          {extension ? (
            <Suspense
              fallback={
                <div className="ps-code-loading">
                  <Spinner /> Loading editor
                </div>
              }
            >
              <CodeMirror
                value={text}
                onChange={setText}
                extensions={[extension]}
                theme={theme === 'dark' ? 'dark' : 'light'}
                readOnly={readonly}
                autoFocus
                height="100%"
                basicSetup={{ tabSize: 4, foldGutter: true, highlightActiveLine: true }}
              />
            </Suspense>
          ) : (
            <div className="ps-code-loading">
              <Spinner /> Loading editor
            </div>
          )}
        </div>
      </Modal>
      {confirm && (
        <ConfirmDialog
          title="Discard your edits?"
          tone="danger"
          confirmLabel="Discard"
          cancelLabel="Keep editing"
          onCancel={() => setConfirm(false)}
          onConfirm={() => {
            setConfirm(false)
            onClose()
          }}
        >
          <p>The changes in this editor have not been applied to the node.</p>
        </ConfirmDialog>
      )}
    </>
  )
}
