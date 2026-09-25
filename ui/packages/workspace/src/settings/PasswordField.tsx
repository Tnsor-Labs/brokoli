import { Check, X } from 'lucide-react'
import { passwordProblems } from '@brokoli/auth'
import { Field, Input } from '@brokoli/ui'

/** The server's password policy (api/users.go validatePassword): 10+ characters, upper, lower and a digit. */
export function passwordOk(password: string) {
  return Object.values(passwordProblems(password)).every(Boolean)
}

export function PasswordField({ label, value, onChange, autoFocus }: { label: string; value: string; onChange: (v: string) => void; autoFocus?: boolean }) {
  const policy = passwordProblems(value)
  return (
    <div className="ws-password">
      <Field label={label}>
        <Input type="password" autoComplete="new-password" value={value} onChange={(e) => onChange(e.target.value)} autoFocus={autoFocus} />
      </Field>
      <ul className="ws-policy" aria-label="Password requirements">
        {(
          [
            ['length', 'At least 10 characters'],
            ['upper', 'An uppercase letter'],
            ['lower', 'A lowercase letter'],
            ['digit', 'A number'],
          ] as const
        ).map(([key, text]) => (
          <li key={key} className={policy[key] ? 'is-met' : ''}>
            {policy[key] ? <Check size={13} aria-hidden="true" /> : <X size={13} aria-hidden="true" />}
            {text}
          </li>
        ))}
      </ul>
    </div>
  )
}
