#!/usr/bin/env node
/*
 * Fails when a stylesheet outside the theme uses a literal color.
 *
 * Every color in the product must come from a --bk-* token so the
 * legibility floor enforced by packages/theme/tokens.test.ts actually
 * reaches the screen. A hard-coded grey is exactly how the previous
 * interface ended up at 2.4:1.
 *
 * Checks every .css file under apps/ and packages/ except the theme
 * package. Literal colors: #rgb/#rrggbb/#rrggbbaa, rgb(), rgba(), hsl(),
 * hsla(). `transparent`, `currentColor` and color-mix() over tokens are fine.
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('..', import.meta.url))
const scanned = []
const findings = []
const LITERAL = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?)\s*\(/g

function walk(dir) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name.startsWith('.')) continue
    const path = join(dir, name)
    if (statSync(path).isDirectory()) walk(path)
    else if (name.endsWith('.css')) scanned.push(path)
  }
}

walk(join(root, 'apps'))
walk(join(root, 'packages'))
const files = scanned.filter((f) => !relative(root, f).startsWith(join('packages', 'theme')))

for (const file of files) {
  const lines = readFileSync(file, 'utf8').split('\n')
  lines.forEach((line, i) => {
    const code = line.replace(/\/\*.*?\*\//g, '')
    for (const m of code.matchAll(LITERAL)) findings.push(`${relative(root, file)}:${i + 1}: ${m[0]} in "${line.trim()}"`)
  })
}

// A scan that looked at nothing proves nothing.
if (files.length < 5) {
  console.error(`check-raw-colors: only ${files.length} stylesheets found; the scan is not looking where it should`)
  process.exit(2)
}
if (findings.length) {
  console.error(`check-raw-colors: ${findings.length} literal color(s); use a --bk-* token instead:\n${findings.join('\n')}`)
  process.exit(1)
}
console.log(`check-raw-colors: ${files.length} stylesheets, no literal colors`)
