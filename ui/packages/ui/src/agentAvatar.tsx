import type { ReactNode } from 'react'

/*
 * Agent avatars: eight of the broccoli family, drawn from the Brokoli mark
 * (round florets, a forked stem, the brand greens), each with a face of its
 * own. The server gives every agent one (Agent.avatar): its owner's choice,
 * or else one drawn from its id. Each sits on a tinted tile, so it reads on
 * light and dark pages and at 20px in a table row.
 */

export type AgentAvatarName =
  | 'broccoli'
  | 'romanesco'
  | 'cauliflower'
  | 'purple'
  | 'broccolini'
  | 'sprout'
  | 'kohlrabi'
  | 'seedling'

type Mood =
  'steady' | 'curious' | 'serene' | 'playful' | 'alert' | 'determined' | 'focused' | 'cheerful'

interface Spec {
  label: string
  mood: Mood
  /** Left eye x, right eye x, eye y, on the 40x40 drawing. */
  eyes: [number, number, number]
  draw: ReactNode
}

const stroke = { strokeLinecap: 'round' as const, fill: 'none' }

const SPECS: Record<AgentAvatarName, Spec> = {
  broccoli: {
    label: 'Broccoli',
    mood: 'steady',
    eyes: [17.1, 22.9, 14.8],
    draw: (
      <>
        <path
          d="M20 35V24M20 27l-6-5M20 29l6.5-5.5"
          stroke="#1d9e66"
          strokeWidth={2.4}
          {...stroke}
        />
        <circle cx={20} cy={6.4} r={4.4} fill="#46efad" />
        <circle cx={12.4} cy={9.4} r={5} fill="#24996f" />
        <circle cx={27.6} cy={9.4} r={5} fill="#24996f" />
        <circle cx={9} cy={17.2} r={4.8} fill="#31d89b" />
        <circle cx={31} cy={17.2} r={4.8} fill="#31d89b" />
        <circle cx={20} cy={15.6} r={8} fill="#1cae7a" />
      </>
    ),
  },
  romanesco: {
    label: 'Romanesco',
    mood: 'curious',
    eyes: [17.3, 22.7, 21],
    draw: (
      <>
        <path d="M20 36v-6" stroke="#6f9a2a" strokeWidth={2.4} {...stroke} />
        <circle cx={20} cy={7} r={3} fill="#c3e66b" />
        <circle cx={16.6} cy={12.6} r={3.4} fill="#a8d64a" />
        <circle cx={23.4} cy={12.6} r={3.4} fill="#a8d64a" />
        <circle cx={13} cy={18.8} r={3.8} fill="#8fc63c" />
        <circle cx={20} cy={18.2} r={3.8} fill="#9ccf42" />
        <circle cx={27} cy={18.8} r={3.8} fill="#8fc63c" />
        <circle cx={9.8} cy={25.4} r={4} fill="#7bb233" />
        <circle cx={16.6} cy={24.8} r={4} fill="#84bb37" />
        <circle cx={23.4} cy={24.8} r={4} fill="#84bb37" />
        <circle cx={30.2} cy={25.4} r={4} fill="#7bb233" />
      </>
    ),
  },
  cauliflower: {
    label: 'Cauliflower',
    mood: 'serene',
    eyes: [16.8, 23.2, 18],
    draw: (
      <>
        <path
          d="M6 26c4-2 7 1 9 6M34 26c-4-2-7 1-9 6"
          stroke="#4f9e5f"
          strokeWidth={5}
          {...stroke}
        />
        <path d="M20 35v-5" stroke="#4f9e5f" strokeWidth={2.4} {...stroke} />
        <circle cx={12.5} cy={20} r={5.4} fill="#e9dcb4" />
        <circle cx={27.5} cy={20} r={5.4} fill="#e9dcb4" />
        <circle cx={15.5} cy={13.5} r={5.6} fill="#f3e9c8" />
        <circle cx={24.5} cy={13.5} r={5.6} fill="#f3e9c8" />
        <circle cx={20} cy={19.5} r={6.4} fill="#f7f0d7" />
      </>
    ),
  },
  purple: {
    label: 'Purple sprouting',
    mood: 'playful',
    eyes: [17.2, 22.8, 14.6],
    draw: (
      <>
        <path
          d="M20 35V23M20 26l-7-6M20 28l7.5-6.5"
          stroke="#4e9a5a"
          strokeWidth={2.2}
          {...stroke}
        />
        <circle cx={20} cy={6} r={3.6} fill="#b285ea" />
        <circle cx={13.6} cy={9} r={4.6} fill="#7c45c4" />
        <circle cx={26.4} cy={9} r={4.6} fill="#7c45c4" />
        <circle cx={10.2} cy={16.4} r={4.4} fill="#9a63d9" />
        <circle cx={29.8} cy={16.4} r={4.4} fill="#9a63d9" />
        <circle cx={20} cy={15.2} r={7.6} fill="#8a52cf" />
      </>
    ),
  },
  broccolini: {
    label: 'Broccolini',
    mood: 'alert',
    eyes: [17.6, 22.4, 10.5],
    draw: (
      <>
        <path d="M20 36V14M20 36L11 15M20 36l9-21" stroke="#2fa27a" strokeWidth={2} {...stroke} />
        <circle cx={10} cy={12} r={3.6} fill="#2fbf8a" />
        <circle cx={7.6} cy={9} r={2.4} fill="#5ad9a8" />
        <circle cx={30} cy={12} r={3.6} fill="#2fbf8a" />
        <circle cx={32.4} cy={9} r={2.4} fill="#5ad9a8" />
        <circle cx={20} cy={10} r={4.6} fill="#22a877" />
        <circle cx={20} cy={5} r={2.8} fill="#5ad9a8" />
      </>
    ),
  },
  sprout: {
    label: 'Brussels sprout',
    mood: 'determined',
    eyes: [16.6, 23.4, 19.5],
    draw: (
      <>
        <circle cx={20} cy={21} r={12} fill="#5fb04a" />
        <path d="M10 17c4 6 16 6 20 0" stroke="#86cc5e" strokeWidth={2.6} {...stroke} />
        <path d="M12.5 26c3.5 3.5 11.5 3.5 15 0" stroke="#3f8f37" strokeWidth={2.4} {...stroke} />
        <path
          d="M20 9c-2 3-2 6 0 9M20 9c2 3 2 6 0 9"
          stroke="#3f8f37"
          strokeWidth={2}
          {...stroke}
        />
      </>
    ),
  },
  kohlrabi: {
    label: 'Kohlrabi',
    mood: 'focused',
    eyes: [16.8, 23.2, 24],
    draw: (
      <>
        <path d="M14 18L9 7M20 17V5M26 18l5-11" stroke="#5aa25f" strokeWidth={2} {...stroke} />
        <circle cx={9} cy={6} r={2.6} fill="#6cc06f" />
        <circle cx={20} cy={4.6} r={2.6} fill="#6cc06f" />
        <circle cx={31} cy={6} r={2.6} fill="#6cc06f" />
        <circle cx={20} cy={25} r={10} fill="#7d5ec9" />
        <path d="M14 22c3-2 9-2 12 0" stroke="#a48be0" strokeWidth={2} {...stroke} />
        <path d="M20 35v1.5" stroke="#5aa25f" strokeWidth={2} {...stroke} />
      </>
    ),
  },
  seedling: {
    label: 'Seedling',
    mood: 'cheerful',
    eyes: [17.9, 22.1, 14.6],
    draw: (
      <>
        <path d="M20 36V18" stroke="#4e9a46" strokeWidth={2.4} {...stroke} />
        <path d="M20 20c-1-7-7-10-13-9 0 6 5 10 13 9z" fill="#6cc25a" />
        <path d="M20 18c1-7 7-10 13-9 0 6-5 10-13 9z" fill="#8bd36b" />
        <circle cx={20} cy={15} r={5.4} fill="#b5e48f" />
      </>
    ),
  },
}

/** Every avatar an agent can have, in the server's order (api.AgentAvatars). */
export const AGENT_AVATARS = (Object.keys(SPECS) as AgentAvatarName[]).map((name) => ({
  name,
  label: SPECS[name].label,
  mood: SPECS[name].mood,
}))

export function isAgentAvatar(name: string | null | undefined): name is AgentAvatarName {
  return !!name && Object.prototype.hasOwnProperty.call(SPECS, name)
}

const INK = 'var(--bk-color-avatar-eye)'

function Face({ mood, eyes: [L, R, y] }: { mood: Mood; eyes: Spec['eyes'] }) {
  const mid = (L + R) / 2
  const my = y + 3.1
  const line = (d: string, w = 1.1) => (
    <path d={d} stroke={INK} strokeWidth={w} strokeLinecap="round" fill="none" />
  )
  const dot = (x: number, r = 1.7) => (
    <>
      <circle cx={x} cy={y} r={r} fill={INK} />
      <circle cx={x + 0.55} cy={y - 0.6} r={0.55} fill="#fff" />
    </>
  )
  switch (mood) {
    case 'curious':
      return (
        <>
          {dot(L)}
          {dot(R)}
          {line(`M${R - 1.6} ${y - 3.2}l3 -1`)}
          <circle cx={mid} cy={my + 0.3} r={0.9} fill={INK} />
        </>
      )
    case 'serene':
      return (
        <>
          {line(`M${L - 1.6} ${y}q1.6 -1.8 3.2 0`)}
          {line(`M${R - 1.6} ${y}q1.6 -1.8 3.2 0`)}
          {line(`M${mid - 1.8} ${my}q1.8 1.6 3.6 0`)}
        </>
      )
    case 'playful':
      return (
        <>
          {dot(L)}
          {line(`M${R - 1.6} ${y}q1.6 1.4 3.2 0`, 1.3)}
          {line(`M${mid - 1.2} ${my + 0.3}q1.8 1 3 -0.8`)}
        </>
      )
    case 'alert':
      return (
        <>
          <circle cx={L} cy={y} r={2.1} fill="#fff" />
          <circle cx={R} cy={y} r={2.1} fill="#fff" />
          <circle cx={L} cy={y} r={1.2} fill={INK} />
          <circle cx={R} cy={y} r={1.2} fill={INK} />
          {line(`M${mid - 1.2} ${my + 0.4}h2.4`)}
        </>
      )
    case 'determined':
      return (
        <>
          {dot(L)}
          {dot(R)}
          {line(`M${L - 1.8} ${y - 3.2}l3.2 0.7`)}
          {line(`M${R + 1.8} ${y - 3.2}l-3.2 0.7`)}
          <path d={`M${mid - 2.4} ${my}h4.8q-2.4 2.8 -4.8 0z`} fill={INK} />
        </>
      )
    case 'focused':
      return (
        <>
          <path d={`M${L - 1.7} ${y}a1.7 1.7 0 0 0 3.4 0z`} fill={INK} />
          <path d={`M${R - 1.7} ${y}a1.7 1.7 0 0 0 3.4 0z`} fill={INK} />
          {line(`M${L - 1.9} ${y}h3.8`)}
          {line(`M${R - 1.9} ${y}h3.8`)}
          {line(`M${mid - 1.5} ${my + 0.1}q1.5 0.9 3 0`)}
        </>
      )
    case 'cheerful':
      return (
        <>
          {dot(L, 1.3)}
          {dot(R, 1.3)}
          <circle cx={L - 1.5} cy={y + 2.2} r={1.1} fill="#f29aa8" opacity={0.85} />
          <circle cx={R + 1.5} cy={y + 2.2} r={1.1} fill="#f29aa8" opacity={0.85} />
          {line(`M${mid - 1.3} ${y + 2.2}q1.3 1.3 2.6 0`)}
        </>
      )
    default:
      return (
        <>
          {dot(L)}
          {dot(R)}
          {line(`M${mid - 1.8} ${my}q1.8 1.6 3.6 0`)}
        </>
      )
  }
}

/**
 * An agent's avatar. An unknown name (an older server, a value from
 * elsewhere) draws the broccoli, the mark itself.
 */
export function AgentAvatar({
  avatar,
  size = 32,
  label,
  className,
}: {
  avatar: string | null | undefined
  size?: number
  /** The accessible name; omit when the agent's name is next to it. */
  label?: string
  className?: string
}) {
  const name: AgentAvatarName = isAgentAvatar(avatar) ? avatar : 'broccoli'
  const spec = SPECS[name]
  return (
    <svg
      className={className}
      viewBox="0 0 40 40"
      width={size}
      height={size}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      focusable="false"
      style={{ flex: 'none', display: 'block' }}
    >
      <rect width={40} height={40} rx={11} fill={`var(--bk-color-avatar-${name})`} />
      {spec.draw}
      <Face mood={spec.mood} eyes={spec.eyes} />
    </svg>
  )
}
