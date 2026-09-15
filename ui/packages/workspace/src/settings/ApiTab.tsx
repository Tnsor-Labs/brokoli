import { useState } from 'react'
import { Copy } from 'lucide-react'
import { Badge, IconButton, SegmentedControl, useToast } from '@brokoli/ui'

/*
 * API reference. Every route, header and response below was checked
 * against core main; anything the previous page claimed that the server
 * does not do (a ?token= websocket parameter, event topics, an assert CLI
 * route) is deliberately absent.
 */
const ENDPOINTS: [string, string, string][] = [
  ['POST', '/api/auth/login', 'Sign in with {username, password}; returns {token} valid for 24 hours'],
  ['GET', '/api/pipelines', 'List pipelines'],
  ['POST', '/api/pipelines', 'Create a pipeline from JSON'],
  ['POST', '/api/pipelines/import', 'Import a YAML or JSON pipeline (1 MiB at most)'],
  ['GET', '/api/pipelines/{id}/export', 'Export a pipeline as YAML'],
  ['POST', '/api/pipelines/{id}/run', 'Start a run; returns 202 {id, pipeline_id, status}'],
  ['POST', '/api/pipelines/{id}/webhook', 'Start a run with the pipeline webhook token; returns {run_id, status}'],
  ['GET', '/api/pipelines/{id}/runs', 'Run history (?limit= and ?after= page through all of it)'],
  ['GET', '/api/runs/{id}', 'A run with its node results'],
  ['GET', '/api/runs/{id}/logs', 'Every log line of a run'],
  ['POST', '/api/runs/{id}/cancel', 'Cancel a pending or running run'],
  ['GET', '/api/connections', 'List connections (credentials are never returned)'],
  ['GET', '/api/variables', 'List variables (secret values are never returned)'],
  ['GET', '/api/scheduler/status', 'Scheduled pipelines and their next run'],
]

function Snippet({ text }: { text: string }) {
  const toast = useToast()
  return (
    <div className="ws-snippet">
      <pre>{text}</pre>
      <IconButton
        size="sm"
        label="Copy"
        onClick={() =>
          navigator.clipboard
            .writeText(text)
            .then(() => toast.success('Copied'))
            .catch((e) => toast.error('Could not copy', e))
        }
      >
        <Copy size={14} aria-hidden="true" />
      </IconButton>
    </div>
  )
}

export function ApiTab() {
  const base = window.location.origin
  const [lang, setLang] = useState<'curl' | 'python' | 'webhook' | 'cli'>('curl')
  const examples = {
    curl: `TOKEN=$(curl -s -X POST ${base}/api/auth/login \\
  -H 'Content-Type: application/json' \\
  -d '{"username":"you","password":"..."}' | jq -r .token)

curl -s ${base}/api/pipelines -H "Authorization: Bearer $TOKEN"
curl -s -X POST ${base}/api/pipelines/PIPELINE_ID/run -H "Authorization: Bearer $TOKEN"`,
    python: `import requests

base = "${base}"
token = requests.post(f"{base}/api/auth/login", json={"username": "you", "password": "..."}).json()["token"]
headers = {"Authorization": f"Bearer {token}"}

run = requests.post(f"{base}/api/pipelines/PIPELINE_ID/run", headers=headers).json()
status = requests.get(f"{base}/api/runs/{run['id']}", headers=headers).json()["status"]`,
    webhook: `# Generate the token in the pipeline editor: Pipeline settings, Webhook trigger.
curl -s -X POST ${base}/api/pipelines/PIPELINE_ID/webhook \\
  -H "X-Webhook-Token: whk_..."
# Responds {"run_id": "...", "status": "pending"}. At most one call every 10 seconds per pipeline.`,
    cli: `brokoli run PIPELINE_ID --server ${base} --follow`,
  }
  return (
    <div className="ws-tab">
      <section className="ws-card">
        <h3>Base URL</h3>
        <Snippet text={`${base}/api`} />
        <p className="ws-muted">
          Scripts authenticate with <code>Authorization: Bearer TOKEN</code>, using the token from <code>POST /api/auth/login</code>. A server started with <code>--api-key</code> requires
          that key on every API request instead.
        </p>
      </section>
      <section className="ws-card">
        <h3>Examples</h3>
        <SegmentedControl
          label="Example language"
          size="sm"
          value={lang}
          onChange={setLang}
          options={[
            { value: 'curl', label: 'curl' },
            { value: 'python', label: 'Python' },
            { value: 'webhook', label: 'Webhook' },
            { value: 'cli', label: 'CLI' },
          ]}
        />
        <Snippet text={examples[lang]} />
      </section>
      <section className="ws-card">
        <h3>Common endpoints</h3>
        <div className="bk-table-wrap">
          <table className="bk-table">
            <tbody>
              {ENDPOINTS.map(([method, path, what]) => (
                <tr key={`${method} ${path}`}>
                  <td>
                    <Badge tone={method === 'GET' ? 'accent' : 'warning'}>{method}</Badge>
                  </td>
                  <td className="bk-mono ws-small">{path}</td>
                  <td>{what}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="ws-muted ws-small">
          Live state is served on <code>/api/ws</code> with the SODP protocol, which clients use to watch state keys; it is not an event-topic stream.
        </p>
      </section>
    </div>
  )
}
