import { useRef, useState, type ChangeEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, Package, PackagePlus, RefreshCw, Trash2 } from 'lucide-react'
import { ApiError, pluginApi, type Plugin, type PluginInstallResult } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Badge, Button, Callout, ConfirmDialog, EmptyState, IconButton, Page, PageHeader, Skeleton, errorMessage, useToast } from '@brokoli/ui'
import '../workspace.css'

const disabled = (e: unknown) => e instanceof ApiError && e.status === 503

/*
 * Plugins add node types to the server. They are installed host-wide (every
 * workspace on the server sees them), verified by the digest the package
 * declares, and their `spec` command runs on the server during install.
 */
export function PluginsPage() {
  const session = useSession()
  const toast = useToast()
  const queryClient = useQueryClient()
  const installed = useQuery({ queryKey: ['plugins'], queryFn: pluginApi.list, retry: false })
  const index = useQuery({ queryKey: ['plugins', 'index'], queryFn: pluginApi.index, retry: false, staleTime: 5 * 60_000 })
  const [uploading, setUploading] = useState(false)
  const [installing, setInstalling] = useState<Set<string>>(new Set())
  const [removing, setRemoving] = useState<Plugin | null>(null)
  const file = useRef<HTMLInputElement>(null)
  const canManage = session.can('settings.edit')
  const unavailable = disabled(installed.error)
  const byName = new Map((installed.data ?? []).map((p) => [p.name, p]))

  const report = (r: PluginInstallResult) => {
    if (r.warning)
      toast.warning(`${r.plugin.name} ${r.plugin.version} is installed but not loaded`, `${r.warning} Its node types become available after the server reloads plugins.`)
    else toast.success(`Installed ${r.plugin.name} ${r.plugin.version}`)
    void queryClient.invalidateQueries({ queryKey: ['plugins'] })
  }

  const upload = async (e: ChangeEvent<HTMLInputElement>) => {
    const chosen = e.target.files?.[0]
    e.target.value = ''
    if (!chosen) return
    setUploading(true)
    try {
      report(await pluginApi.install(chosen))
    } catch (err) {
      toast.error(`Could not install ${chosen.name}`, err)
    } finally {
      setUploading(false)
    }
  }

  const installByName = async (name: string) => {
    setInstalling((s) => new Set(s).add(name))
    try {
      report(await pluginApi.installByName(name))
    } catch (err) {
      toast.error(`Could not install ${name}`, err)
    } finally {
      setInstalling((s) => {
        const next = new Set(s)
        next.delete(name)
        return next
      })
    }
  }

  return (
    <Page>
      <input ref={file} type="file" accept=".bkg" hidden onChange={upload} />
      <PageHeader
        eyebrow="Server"
        title="Plugins"
        description="Packages that add node types. They are installed for the whole server, and only administrators can install or remove them."
        actions={
          canManage &&
          !unavailable && (
            <Button variant="primary" icon={<PackagePlus size={16} aria-hidden="true" />} loading={uploading} onClick={() => file.current?.click()}>
              Install from file
            </Button>
          )
        }
      />

      <section className="ws-section">
        <header className="ws-section-head">
          <h2>Installed</h2>
          {installed.data && <span className="ws-muted">{installed.data.length} package{installed.data.length === 1 ? '' : 's'}</span>}
        </header>
        {installed.isPending ? (
          <div className="bk-table-wrap ws-loading">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} height={22} />
            ))}
          </div>
        ) : unavailable ? (
          <Callout tone="info" title="Plugin support is turned off on this server">
            The server was started without a plugin directory, so packages cannot be installed or used.
          </Callout>
        ) : installed.isError ? (
          <Callout tone="danger" title="Installed plugins could not be loaded" action={<Button size="sm" onClick={() => void installed.refetch()}>Try again</Button>}>
            {errorMessage(installed.error)}
          </Callout>
        ) : !installed.data.length ? (
          <div className="ws-empty">
            <EmptyState icon={<Package size={20} aria-hidden="true" />} title="No plugins installed">
              Install a .bkg package from a file, or pick one from the catalog below.
            </EmptyState>
          </div>
        ) : (
          <div className="bk-table-wrap">
            <table className="bk-table ws-table">
              <thead>
                <tr>
                  <th scope="col">Package</th>
                  <th scope="col">Version</th>
                  <th scope="col">Node types</th>
                  <th scope="col">Installed from</th>
                  <th scope="col">
                    <span className="bk-sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {installed.data.map((p) => (
                  <tr key={p.name}>
                    <td>
                      <div className="ws-name">
                        <span className="ws-type-icon">
                          <Package size={16} aria-hidden="true" />
                        </span>
                        <span>
                          <code>{p.name}</code>
                          <small>{p.description || 'No description'}</small>
                        </span>
                      </div>
                    </td>
                    <td className="bk-mono">{p.version || '-'}</td>
                    <td>
                      <div className="ws-chips">
                        {(p.node_types ?? []).map((n) => (
                          <span key={n.type} className="ws-chip" title={n.kind === 'transform' ? 'Transform node types are listed but the current engine only executes source and sink plugin nodes' : undefined}>
                            {n.display_name || n.type}
                            <Badge tone={n.kind === 'transform' ? 'warning' : 'neutral'}>{n.kind}</Badge>
                          </span>
                        ))}
                      </div>
                    </td>
                    <td>{p.packaged ? <Badge title={p.archive_sha256 ? `sha256 ${p.archive_sha256}` : undefined}>Package</Badge> : <Badge tone="queued">Directory</Badge>}</td>
                    <td>
                      {canManage && (
                        <div className="ws-actions">
                          <IconButton size="sm" variant="danger" label={`Remove ${p.name}`} onClick={() => setRemoving(p)}>
                            <Trash2 size={15} aria-hidden="true" />
                          </IconButton>
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {!unavailable && (
        <section className="ws-section">
          <header className="ws-section-head">
            <h2>Catalog</h2>
            <span className="ws-muted">Packages from the configured plugin index, checked against the digest it publishes.</span>
          </header>
          {index.isPending ? (
            <div className="bk-table-wrap ws-loading">
              {Array.from({ length: 3 }, (_, i) => (
                <Skeleton key={i} height={22} />
              ))}
            </div>
          ) : index.isError ? (
            <Callout tone="warning" title="The catalog could not be reached" action={<Button size="sm" icon={<RefreshCw size={14} aria-hidden="true" />} onClick={() => void index.refetch()}>Check again</Button>}>
              {errorMessage(index.error)}. You can still install a package from a file.
            </Callout>
          ) : !index.data.plugins?.length ? (
            <p className="ws-muted">The catalog is reachable but lists no packages.</p>
          ) : (
            <div className="bk-table-wrap">
              <table className="bk-table ws-table">
                <thead>
                  <tr>
                    <th scope="col">Package</th>
                    <th scope="col">Version</th>
                    <th scope="col">
                      <span className="bk-sr-only">Status</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {index.data.plugins.map((entry) => {
                    const have = byName.get(entry.name)
                    return (
                      <tr key={entry.name}>
                        <td>
                          <div className="ws-name">
                            <span className="ws-type-icon is-other">
                              <Download size={16} aria-hidden="true" />
                            </span>
                            <span>
                              <code>{entry.name}</code>
                              <small>{entry.description || 'No description'}</small>
                            </span>
                          </div>
                        </td>
                        <td className="bk-mono">{entry.version}</td>
                        <td>
                          <div className="ws-actions">
                            {have ? (
                              <Badge tone={have.version === entry.version ? 'success' : 'warning'}>
                                {have.version === entry.version ? 'Installed' : `Installed ${have.version}; remove it to install ${entry.version}`}
                              </Badge>
                            ) : (
                              canManage && (
                                <Button size="sm" loading={installing.has(entry.name)} onClick={() => void installByName(entry.name)}>
                                  Install
                                </Button>
                              )
                            )}
                          </div>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}

      {removing && (
        <ConfirmDialog
          title={`Remove ${removing.name}?`}
          tone="danger"
          confirmLabel="Remove plugin"
          confirmText={removing.name}
          onCancel={() => setRemoving(null)}
          onConfirm={async () => {
            await pluginApi.remove(removing.name)
            await queryClient.invalidateQueries({ queryKey: ['plugins'] })
            toast.success(`Removed ${removing.name}`)
            setRemoving(null)
          }}
        >
          <p>
            Pipelines that use {(removing.node_types ?? []).map((n) => n.display_name || n.type).join(', ') || 'its node types'} fail on their next run until it is installed again. The server does not check for such pipelines.
          </p>
        </ConfirmDialog>
      )}
    </Page>
  )
}
