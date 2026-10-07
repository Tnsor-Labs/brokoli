import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import ReactMarkdown from 'react-markdown'
import { Check, Copy, Cpu, ExternalLink, Plane, RefreshCw, Trash2 } from 'lucide-react'
import { driverApi, type DriverCatalogEntry } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Badge, Button, Callout, ConfirmDialog, EmptyState, IconButton, Modal, Page, PageHeader, Skeleton, errorMessage, useToast } from '@brokoli/ui'
import '../workspace.css'

export function DriversPage() {
  const session = useSession()
  const toast = useToast()
  const queryClient = useQueryClient()
  const catalog = useQuery({ queryKey: ['drivers', 'catalog'], queryFn: driverApi.catalog, retry: false, staleTime: 5 * 60_000 })
  const [installing, setInstalling] = useState<Set<string>>(new Set())
  const [removing, setRemoving] = useState<DriverCatalogEntry | null>(null)
	const [selected, setSelected] = useState<DriverCatalogEntry | null>(null)
  // Installing a driver changes what every worker on the host loads, for
  // every workspace: it takes drivers.manage, not workspace settings.
  const canManage = session.can('drivers.manage')
  const [copied, setCopied] = useState(false)
	const docs = useQuery({
		queryKey: ['drivers', 'docs-v2', selected?.name, selected?.version],
		enabled: Boolean(selected?.docs_url),
		queryFn: () => driverApi.documentation(selected!.name, selected!.version),
		staleTime: 5 * 60_000,
	})

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['drivers', 'catalog'] })
  const install = async (entry: DriverCatalogEntry) => {
	const key = `${entry.name}:${entry.version}`
	setInstalling((current) => new Set(current).add(key))
    try {
	  const installed = await driverApi.install(entry.name, entry.version)
      toast.success(`Installed ${installed.name} ${installed.version}`)
      await refresh()
    } catch (err) {
	  if (errorMessage(err).includes('already installed')) {
		toast.success(`${entry.name} ${entry.version} is already installed`)
		await refresh()
		return
	  }
      toast.error(`Could not install ${entry.name}`, err)
    } finally {
      setInstalling((current) => {
        const next = new Set(current)
		next.delete(key)
        return next
      })
    }
  }

  return (
    <Page>
      <PageHeader
        eyebrow="Server"
        title="Drivers"
        description="Native ADBC drivers installed on this server. A Flight SQL, PostgreSQL or SQLite connection can be pinned to one exact build, which its sources then read through in an isolated worker process."
      />

      <section className="ws-section">
        <header className="ws-section-head">
			<h2>Curated catalog</h2>
			{catalog.data && <span className="ws-muted">{catalog.data.platform.os}/{catalog.data.platform.arch}</span>}
			<Button size="sm" variant="ghost" loading={catalog.isFetching} icon={<RefreshCw size={14} aria-hidden="true" />} onClick={() => void catalog.refetch()}>Refresh</Button>
        </header>
        {catalog.data && !catalog.data.native_worker_enabled && (
          <Callout tone="warning" title="This server cannot run native drivers">
            This build of Brokoli was compiled without native driver support. Drivers installed here can still be pinned by connections, but those connections only run on a worker built with native driver support.
          </Callout>
        )}
        {catalog.data && !catalog.data.configured && (
          <Callout tone="info" title="No driver catalog configured">
            Drivers installed on this server are listed below. To install from a curated catalog, an operator sets <code>BROKOLI_DRIVER_INDEX</code> to its URL. There is no default: the catalog decides which native code an install loads, and it is not signed yet.
          </Callout>
        )}
        {catalog.isPending ? (
          <div className="bk-table-wrap ws-loading">
            {Array.from({ length: 3 }, (_, i) => <Skeleton key={i} height={22} />)}
          </div>
        ) : catalog.isError ? (
          <Callout tone="warning" title="The driver catalog could not be reached" action={<Button size="sm" icon={<RefreshCw size={14} aria-hidden="true" />} onClick={() => void catalog.refetch()}>Check again</Button>}>
            {errorMessage(catalog.error)}
          </Callout>
        ) : !catalog.data?.drivers.length ? (
          <div className="ws-empty">
            <EmptyState icon={<Cpu size={20} aria-hidden="true" />} title="No compatible drivers in the catalog">
              This catalog has no package for this server&apos;s platform.
            </EmptyState>
          </div>
        ) : (
          <div className="bk-table-wrap">
            <table className="bk-table ws-table">
              <thead><tr><th scope="col">Driver</th><th scope="col">Version</th><th scope="col">Verification</th><th scope="col"><span className="bk-sr-only">Actions</span></th></tr></thead>
              <tbody>
                {catalog.data.drivers.map((entry) => (
                  <tr key={`${entry.name}:${entry.version}`}>
                    <td>
						<div className="ws-name"><span className="ws-type-icon is-other"><DriverIcon name={entry.name} icon={entry.icon} iconURL={entry.icon_url} /></span><span><Button variant="ghost" size="sm" onClick={() => setSelected(entry)}>{entry.display_name ?? entry.name}</Button><small>{entry.description ?? 'Native ADBC driver'}</small><small className="ws-muted">{usableByText(entry.usable_by)}</small></span></div>
                    </td>
                    <td className="bk-mono">{entry.version}</td>
					<td><div className="ws-chips"><Badge tone={entry.lifecycle === 'revoked' ? 'danger' : entry.lifecycle === 'deprecated' ? 'warning' : entry.installed ? 'success' : 'neutral'}>{entry.lifecycle ?? (entry.installed ? 'Installed' : 'SHA-256 pinned')}</Badge>{entry.license && <Badge tone="neutral">{entry.license}</Badge>}{entry.advisories?.map((advisory) => <Badge key={advisory.id} tone="danger" title={advisory.summary}>{advisory.id}</Badge>)}</div></td>
                    <td><div className="ws-actions">
                      {entry.installed ? (
                        canManage && <IconButton size="sm" variant="danger" label={`Remove ${entry.name}`} onClick={() => setRemoving(entry)}><Trash2 size={15} aria-hidden="true" /></IconButton>
						) : canManage && entry.available && entry.lifecycle !== 'revoked' && (entry.usable_by ?? []).length > 0 ? (
						<Button size="sm" loading={installing.has(`${entry.name}:${entry.version}`)} onClick={() => void install(entry)}>Install {entry.version}</Button>
						) : <span className="ws-muted">{entry.lifecycle === 'revoked' ? 'Installation blocked' : !(entry.usable_by ?? []).length ? 'No connection can use it yet' : entry.available ? 'Needs drivers.manage' : 'Installed locally'}</span>}
                    </div></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {removing && (
        <ConfirmDialog title={`Remove ${removing.name}?`} tone="danger" confirmLabel="Remove driver" confirmText={removing.name} onCancel={() => setRemoving(null)} onConfirm={async () => {
			await driverApi.remove(removing.name, removing.version)
          await refresh()
          toast.success(`Removed ${removing.name}`)
          setRemoving(null)
        }}>
          <p>Connections pinned to this driver cannot run until this exact version is installed again.</p>
        </ConfirmDialog>
      )}

	  {selected && (
		<Modal title={`${selected.display_name ?? selected.name} ${selected.version}`} description={selected.description || 'Native ADBC driver release'} size="lg" onClose={() => setSelected(null)}>
		  <div className="driver-detail">
			<div className="driver-detail__hero"><span className="driver-detail__icon"><DriverIcon name={selected.name} icon={selected.icon} iconURL={selected.icon_url} /></span><div><div className="ws-chips"><Badge tone={selected.lifecycle === 'revoked' ? 'danger' : selected.lifecycle === 'deprecated' ? 'warning' : 'success'}>{selected.lifecycle ?? 'supported'}</Badge>{selected.license && <Badge tone="neutral">{selected.license}</Badge>}{selected.installed && <Badge tone="success">Installed</Badge>}</div><p>{usableByText(selected.usable_by)}</p><p>{selected.os}/{selected.arch} <span>ADBC {selected.adbc_version ?? 'unspecified'}</span>{selected.min_brokoli && <span>Brokoli {selected.min_brokoli}+</span>}</p></div></div>
			<div className="driver-detail__grid"><section><h3>Release identity</h3><div className="driver-detail__value"><small>Driver ID</small><code>{selected.name}</code></div><div className="driver-detail__value"><small>Archive SHA-256</small><code title={selected.sha256}>{selected.sha256.slice(0, 16)}...{selected.sha256.slice(-12)}</code><IconButton size="sm" variant="ghost" label={copied ? 'Digest copied' : 'Copy archive digest'} onClick={() => { void navigator.clipboard.writeText(selected.sha256); setCopied(true); window.setTimeout(() => setCopied(false), 1500) }}>{copied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}</IconButton></div></section><section><h3>Resources</h3><div className="driver-detail__links">{selected.homepage && <a href={selected.homepage} target="_blank" rel="noreferrer">Homepage <ExternalLink size={13} /></a>}{selected.docs_url && <a href={selected.docs_url} target="_blank" rel="noreferrer">Documentation <ExternalLink size={13} /></a>}</div><p className="ws-muted">The archive is checked against this digest before it is installed. A connection pins the installed library's own digest, shown when you choose the driver for it.</p></section></div>
			{selected.docs_url && <section className="driver-detail__docs"><h3>Documentation</h3>{docs.isPending ? <Skeleton height={120} /> : docs.isError ? <Callout tone="warning" title="Documentation could not be loaded">{errorMessage(docs.error)}</Callout> : <ReactMarkdown>{docs.data}</ReactMarkdown>}</section>}
			{selected.advisories?.length ? <Callout tone="danger" title="Security advisories">{selected.advisories.map((advisory) => <p key={advisory.id}><strong>{advisory.id} · {advisory.severity}</strong>: {advisory.summary}{advisory.fixed_in ? ` Upgrade to ${advisory.fixed_in}.` : ''}</p>)}</Callout> : <Callout tone="success" title="No published advisories">No security advisory is published for this release.</Callout>}
		  </div>
		</Modal>
	  )}
    </Page>
  )
}

// usableByText says which connection types can read through a driver. The
// server decides (usable_by); a driver it maps to none installs and loads,
// but no connection can be pinned to it yet, so the page says so instead of
// offering an install that would do nothing.
const CONNECTION_TYPE_LABELS: Record<string, string> = {
  flightsql: 'Flight SQL', postgres: 'PostgreSQL', sqlite: 'SQLite', mysql: 'MySQL', clickhouse: 'ClickHouse',
}

function usableByText(usableBy?: string[]) {
  if (!usableBy?.length) return 'Not yet usable by any connection'
  return 'Used by ' + usableBy.map((t) => CONNECTION_TYPE_LABELS[t] ?? t).join(', ') + ' connections'
}

function DriverIcon({ name, icon, iconURL }: { name: string; icon?: string; iconURL?: string }) {
	if (iconURL) return <img className={`driver-icon driver-icon--${name}`} src={iconURL} alt="" />
	return icon === 'flight' ? <Plane size={16} aria-hidden="true" /> : <Cpu size={16} aria-hidden="true" />
}
