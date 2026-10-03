import { composeSecretRef, isSecretRef, type SecretRefParts } from '../secret-stores/reference'

export type PasswordSource = 'stored' | 'store'

/**
 * What a save sends for the password, from the form's choice and the
 * reference the server holds now:
 *
 * - From a secret store: the composed secret:// reference, and no password.
 * - Stored in Brokoli, with a password typed: the password. A stored
 *   secret:// reference is not echoed, so the typed value replaces it.
 * - Stored in Brokoli, nothing typed: the stored reference is echoed back,
 *   which the server reads as "unchanged" (an encrypted:// mask, or an
 *   operator-level env://, vault://, k8s:// reference that stays as it is).
 */
export function passwordFields(
  source: PasswordSource,
  ref: SecretRefParts,
  password: string,
  storedRef: string | undefined,
  operatorRef: boolean,
): { password?: string; password_ref?: string } {
  if (source === 'store') return { password_ref: composeSecretRef(ref) }
  const out: { password?: string; password_ref?: string } = {}
  if (password && !operatorRef) out.password = password
  if (storedRef && !(isSecretRef(storedRef) && password)) out.password_ref = storedRef
  return out
}
