import { useMemo, useState } from 'react'
import { Copy } from 'lucide-react'
import { dump } from 'js-yaml'
import type { Pipeline } from '@brokoli/api'
import { Button, Modal, SegmentedControl, useToast } from '@brokoli/ui'

/*
 * The document exactly as the editor would save it, as YAML or JSON. The
 * webhook token is masked here and in the copy: it is a credential. This
 * is not the server's export format; "Download saved YAML" in the toolbar
 * menu gives that.
 */
export function CodeViewModal({ payload, dirty, onClose }: { payload: Pipeline; dirty: boolean; onClose: () => void }) {
  const toast = useToast()
  const [format, setFormat] = useState<'yaml' | 'json'>('yaml')
  const text = useMemo(() => {
    const safe = payload.webhook_token ? { ...payload, webhook_token: '<redacted>' } : payload
    return format === 'yaml' ? dump(safe, { lineWidth: 120, noRefs: true, sortKeys: false }) : JSON.stringify(safe, null, 2)
  }, [payload, format])
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success('Copied to the clipboard')
    } catch (e) {
      toast.error('Could not copy', e)
    }
  }
  return (
    <Modal
      title="Pipeline document"
      description={dirty ? 'Includes changes that are not saved yet.' : 'Matches the saved pipeline.'}
      size="xl"
      onClose={onClose}
      footer={
        <>
          <SegmentedControl
            label="Format"
            size="sm"
            value={format}
            onChange={setFormat}
            options={[
              { value: 'yaml', label: 'YAML' },
              { value: 'json', label: 'JSON' },
            ]}
          />
          <span className="ps-spacer" />
          <Button icon={<Copy size={14} aria-hidden="true" />} onClick={() => void copy()}>
            Copy
          </Button>
          <Button variant="primary" onClick={onClose}>
            Close
          </Button>
        </>
      }
    >
      <pre className="ps-code-view">{text}</pre>
    </Modal>
  )
}
