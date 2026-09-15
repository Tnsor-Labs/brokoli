import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { PencilLine, Plus, PlugZap, Trash2 } from 'lucide-react'
import { connectionApi, type Connection } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import {
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Page,
  PageHeader,
  Pagination,
  SearchInput,
  Select,
  Skeleton,
  Spinner,
  errorMessage,
  formatRelative,
  useToast,
} from '@brokoli/ui'
import { USABLE_BY_NODES } from './catalog'
import { ConnectionForm } from './ConnectionForm'
import { VendorIcon } from './VendorIcon'
import '../workspace.css'

const PAGE_SIZE = 25

function DeleteConnection({ connection, onClose }: { connection: Connection; onClose: () => void }) {
  const toast = useToast()
  const queryClient = useQueryClient()
  // The server deletes regardless of references, so show them before the user confirms.
  const usage = useQuery({ queryKey: ['connections', connection.conn_id, 'used-by'], queryFn: () => connectionApi.usedBy(connection.conn_id) })
  const pipelines = [...new Set((usage.data ?? []).map((u) => u.pipeline_name))]
  return (
    <ConfirmDialog
      title={`Delete ${connection.conn_id}?`}
      tone="danger"
      confirmLabel="Delete connection"
      confirmText={usage.data?.length ? connection.conn_id : undefined}
      onCancel={onClose}
      onConfirm={async () => {
        await connectionApi.remove(connection.conn_id)
        await queryClient.invalidateQueries({ queryKey: ['connections'] })
        toast.success(`Deleted ${connection.conn_id}`)
        onClose()
      }}
    >
      {usage.isPending ? (
        <p className="ws-inline">
          <Spinner size="sm" /> Checking which pipelines use it
        </p>
      ) : usage.isError ? (
        <Callout tone="warning">Could not check which pipelines use it: {errorMessage(usage.error)}</Callout>
      ) : usage.data.length ? (
        <Callout tone="danger" title={`Used by ${usage.data.length} node${usage.data.length === 1 ? '' : 's'} in ${pipelines.length} pipeline${pipelines.length === 1 ? '' : 's'}`}>
          {pipelines.join(', ')}. Those nodes will run without its credentials until they are pointed at another connection.
        </Callout>
      ) : (
        <p>No node references this connection by ID. Its stored credentials are removed.</p>
      )}
    </ConfirmDialog>
  )
}

export function ConnectionsPage() {
  const session = useSession()
  const toast = useToast()
  const list = useQuery({ queryKey: ['connections'], queryFn: connectionApi.list })
  const types = useQuery({ queryKey: ['connection-types'], queryFn: connectionApi.types, staleTime: 5 * 60_000 })
  const [params] = useSearchParams()
  const [search, setSearch] = useState(() => params.get('q') ?? '')
  // The search palette opens this page with ?q= naming one item; follow it when already here.
  useEffect(() => {
    const q = params.get('q')
    if (q !== null) setSearch(q)
  }, [params])
  const [type, setType] = useState('')
  const [page, setPage] = useState(1)
  const [editing, setEditing] = useState<Connection | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Connection | null>(null)
  const [testing, setTesting] = useState<string | null>(null)

  const metaOf = (t: string) => types.data?.find((m) => m.type === t)
  const all = useMemo(() => list.data ?? [], [list.data])
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return all.filter(
      (c) =>
        (!type || c.type === type) &&
        (!q || `${c.conn_id} ${c.type} ${metaOf(c.type)?.label ?? ''} ${c.host ?? ''} ${c.schema ?? ''} ${c.description ?? ''}`.toLowerCase().includes(q)),
    )
  }, [all, search, type, types.data]) // eslint-disable-line react-hooks/exhaustive-deps
  const pages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE))
  const current = Math.min(page, pages)
  const rows = filtered.slice((current - 1) * PAGE_SIZE, current * PAGE_SIZE)
  const presentTypes = [...new Set(all.map((c) => c.type))].sort()

  const test = async (c: Connection) => {
    setTesting(c.conn_id)
    try {
      const r = await connectionApi.test(c.conn_id)
      if (r.success) toast.success(`${c.conn_id} works`, r.message)
      else toast.error(`${c.conn_id} failed`, r.error || r.message || 'No details from the server')
    } catch (e) {
      toast.error(`Could not test ${c.conn_id}`, e)
    } finally {
      setTesting(null)
    }
  }

  return (
    <Page>
      <PageHeader
        eyebrow="Workspace"
        title="Connections"
        description="Databases, storage and APIs your pipelines use. Credentials are encrypted on the server and never shown again."
        actions={
          session.can('connections.create') && (
            <Button variant="primary" icon={<Plus size={16} aria-hidden="true" />} onClick={() => setEditing('new')}>
              New connection
            </Button>
          )
        }
      />
      {list.isError ? (
        <Callout tone="danger" title="Connections could not be loaded" action={<Button size="sm" onClick={() => void list.refetch()}>Try again</Button>}>
          {errorMessage(list.error)}
        </Callout>
      ) : list.isPending ? (
        <div className="bk-table-wrap ws-loading">
          {Array.from({ length: 5 }, (_, i) => (
            <Skeleton key={i} height={22} />
          ))}
        </div>
      ) : !all.length ? (
        <div className="ws-empty">
          <EmptyState
            icon={<PlugZap size={20} aria-hidden="true" />}
            title="No connections yet"
            action={
              session.can('connections.create') && (
                <Button variant="primary" icon={<Plus size={16} aria-hidden="true" />} onClick={() => setEditing('new')}>
                  Add a connection
                </Button>
              )
            }
          >
            Save a database, bucket or API once, then pick it by ID in any pipeline node.
          </EmptyState>
        </div>
      ) : (
        <section className="ws-list">
          <div className="ws-toolbar">
            <SearchInput
              value={search}
              onChange={(v) => {
                setSearch(v)
                setPage(1)
              }}
              placeholder="Search by ID, type, host or description"
              className="ws-search"
            />
            <Select
              aria-label="Filter by type"
              value={type}
              onChange={(e) => {
                setType(e.target.value)
                setPage(1)
              }}
              className="ws-select"
            >
              <option value="">All types</option>
              {presentTypes.map((t) => (
                <option key={t} value={t}>
                  {metaOf(t)?.label ?? t}
                </option>
              ))}
            </Select>
            <span className="ws-count">
              {filtered.length} of {all.length}
            </span>
          </div>
          {types.isError && <Callout tone="warning">Connection types could not be loaded ({errorMessage(types.error)}); new connections cannot be created until they are.</Callout>}
          {!filtered.length ? (
            <div className="bk-table-wrap">
              <EmptyState
                title="No connections match"
                action={
                  <Button
                    onClick={() => {
                      setSearch('')
                      setType('')
                    }}
                  >
                    Clear filters
                  </Button>
                }
              />
            </div>
          ) : (
            <>
              <div className="bk-table-wrap">
                <table className="bk-table ws-table">
                  <thead>
                    <tr>
                      <th scope="col">Connection</th>
                      <th scope="col">Type</th>
                      <th scope="col">Endpoint</th>
                      <th scope="col">Limit</th>
                      <th scope="col">Updated</th>
                      <th scope="col">
                        <span className="bk-sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((c) => {
                      const meta = metaOf(c.type)
                      return (
                        <tr key={c.conn_id} className="is-clickable" onClick={() => setEditing(c)}>
                          <td>
                            <div className="ws-name">
                              <span className="ws-type-icon is-brand">
                                <VendorIcon type={c.type} category={meta?.category ?? 'other'} size={16} />
                              </span>
                              <span>
                                <code>{c.conn_id}</code>
                                <small>{c.description || 'No description'}</small>
                              </span>
                            </div>
                          </td>
                          <td>
                            <Badge>{meta?.label ?? c.type}</Badge>
                            {!USABLE_BY_NODES.has(c.type) && <small className="ws-note">not usable by nodes</small>}
                          </td>
                          <td className="bk-mono ws-endpoint">
                            {c.host ? `${c.host}${c.port ? `:${c.port}` : ''}` : <span className="ws-muted">Not set</span>}
                            {c.schema && <small>{c.schema}</small>}
                          </td>
                          <td>{c.max_concurrent ? `${c.max_concurrent} at once` : <span className="ws-muted">None</span>}</td>
                          <td>{formatRelative(c.updated_at)}</td>
                          <td>
                            <div className="ws-actions" onClick={(e) => e.stopPropagation()}>
                              {session.can('connections.test') && (
                                <Button size="sm" icon={<PlugZap size={14} aria-hidden="true" />} loading={testing === c.conn_id} onClick={() => void test(c)}>
                                  Test
                                </Button>
                              )}
                              <IconButton size="sm" label={`Edit ${c.conn_id}`} onClick={() => setEditing(c)}>
                                <PencilLine size={15} aria-hidden="true" />
                              </IconButton>
                              {session.can('connections.delete') && (
                                <IconButton size="sm" variant="danger" label={`Delete ${c.conn_id}`} onClick={() => setDeleting(c)}>
                                  <Trash2 size={15} aria-hidden="true" />
                                </IconButton>
                              )}
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
              <Pagination page={current} pageSize={PAGE_SIZE} total={filtered.length} onPage={setPage} label="connections" />
            </>
          )}
        </section>
      )}
      {editing && <ConnectionForm existing={editing === 'new' ? undefined : editing} types={types.data ?? []} onClose={() => setEditing(null)} />}
      {deleting && <DeleteConnection connection={deleting} onClose={() => setDeleting(null)} />}
    </Page>
  )
}
