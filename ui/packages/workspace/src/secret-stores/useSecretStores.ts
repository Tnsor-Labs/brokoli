import { useQuery } from '@tanstack/react-query'
import {
  ApiError,
  secretStoreApi,
  type SecretShape,
  type SecretStore,
  type SecretStoreProvider,
} from '@brokoli/api'

/** True when the server answered that it has no secret stores (501). */
export function storesUnavailable(error: unknown) {
  return error instanceof ApiError && error.status === 501
}

/**
 * The workspace's secret stores and the server's providers, for pickers.
 * `available` is false when the server has no secret stores at all, so a
 * form can leave the option out instead of offering something that fails.
 */
export function useSecretStores() {
  const stores = useQuery({
    queryKey: ['secret-stores'],
    queryFn: secretStoreApi.list,
    retry: (n, e) => !storesUnavailable(e) && n < 2,
  })
  const providers = useQuery({
    queryKey: ['secret-stores', 'providers'],
    queryFn: secretStoreApi.providers,
  })
  const list: SecretStore[] = stores.data ?? []
  const providerList: SecretStoreProvider[] = providers.data ?? []
  const shapeOf = (storeName: string): SecretShape | undefined => {
    const store = list.find((s) => s.name === storeName)
    return store ? providerList.find((p) => p.name === store.provider)?.shape : undefined
  }
  return {
    available: !storesUnavailable(stores.error),
    loading: stores.isPending,
    error: stores.isError && !storesUnavailable(stores.error) ? stores.error : null,
    stores: list,
    providers: providerList,
    shapeOf,
  }
}
