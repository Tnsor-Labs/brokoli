#!/usr/bin/env node
/*
 * Keeps the paid edition out of the community build.
 *
 * This repo holds the shared packages and the community app. The invariant
 * is worth proving here: nothing may import a paid-edition app or package,
 * and the built community bundle must not contain the edition marker that
 * the paid build stamps into its own bundle.
 *
 * Source check: every import specifier in apps/community and packages/*
 * is resolved and must not land in apps/enterprise (or apps/community, for
 * packages). Bundle check (when apps/community/dist exists): the built
 * files must not contain the edition marker below.
 */
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('..', import.meta.url))
const IMPORT = /(?:import|export)\s[^'"]*?from\s*['"]([^'"]+)['"]|import\(\s*['"]([^'"]+)['"]\s*\)|import\s*['"]([^'"]+)['"]/g
const findings = []
let scanned = 0

function sources(dir) {
  const out = []
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name.startsWith('.')) continue
    const path = join(dir, name)
    if (statSync(path).isDirectory()) out.push(...sources(path))
    else if (/\.(tsx?|mjs|js)$/.test(name)) out.push(path)
  }
  return out
}

function check(files, forbidden, label) {
  for (const file of files) {
    scanned++
    const text = readFileSync(file, 'utf8')
    for (const m of text.matchAll(IMPORT)) {
      const spec = m[1] ?? m[2] ?? m[3]
      const target = spec.startsWith('.') ? resolve(dirname(file), spec) : null
      const hit = forbidden.find((f) => (target ? target.startsWith(join(root, f.dir)) : spec === f.pkg || spec.startsWith(`${f.pkg}/`)))
      if (hit) findings.push(`${relative(root, file)} imports "${spec}" (${label} must not depend on ${hit.dir})`)
    }
  }
}

const enterprise = { dir: join('apps', 'enterprise'), pkg: '@brokoli/enterprise' }
const community = { dir: join('apps', 'community'), pkg: '@brokoli/community' }
check(sources(join(root, 'apps', 'community')), [enterprise], 'Community')
check(sources(join(root, 'packages')), [enterprise, community], 'shared packages')

// The paid build stamps this marker into its bundle; the community bundle
// must never contain it. Pinned here as a negative sentinel.
const marker = 'brokoli-enterprise-edition-bundle'
const dist = join(root, 'apps', 'community', 'dist')
let bundleFiles = 0
if (marker && existsSync(dist)) {
  for (const file of sources(dist).concat(readdirSync(join(dist, 'assets')).map((n) => join(dist, 'assets', n)))) {
    if (!/\.(js|mjs|html|css)$/.test(file)) continue
    bundleFiles++
    if (readFileSync(file, 'utf8').includes(marker)) findings.push(`${relative(root, file)} contains the enterprise marker "${marker}"`)
  }
}

if (scanned < 20) {
  console.error(`check-boundaries: only ${scanned} source files scanned; the check is not looking where it should`)
  process.exit(2)
}
if (findings.length) {
  console.error(`check-boundaries: ${findings.length} violation(s):\n${findings.join('\n')}`)
  process.exit(1)
}
console.log(`check-boundaries: ${scanned} source files${bundleFiles ? ` and ${bundleFiles} community bundle files` : ''} clean`)
