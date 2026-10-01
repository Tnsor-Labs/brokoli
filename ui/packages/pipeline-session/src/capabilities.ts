import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { systemApi, type Capabilities } from '@brokoli/api'

const NONE: ReadonlySet<string> = new Set()

/** The node types a server refuses, from its capabilities. Missing or malformed means none. */
export function disabledNodeTypes(capabilities: Capabilities | undefined): ReadonlySet<string> {
  const list = capabilities?.disabled_node_types
  if (!Array.isArray(list)) return NONE
  return new Set(list.filter((t): t is string => typeof t === 'string').map((t) => t.toLowerCase()))
}

/** The server's own words for a refused node type, so the editor and validation say the same thing. */
export function disabledNodeMessage(type: string) {
  return `${type} nodes are disabled on this deployment (BROKOLI_DISABLED_NODE_TYPES)`
}

/**
 * Node types this server refuses. Read once per app load: the setting only
 * changes when the server restarts. If the server cannot say -- an older
 * server, or the request failed -- nothing is hidden, and server validation
 * still refuses what it refuses.
 */
export function useDisabledNodeTypes(): ReadonlySet<string> {
  const capabilities = useQuery({
    queryKey: ['capabilities'],
    queryFn: systemApi.capabilities,
    staleTime: Infinity,
    gcTime: Infinity,
    retry: false,
    refetchOnWindowFocus: false,
  })
  return useMemo(() => disabledNodeTypes(capabilities.data), [capabilities.data])
}
