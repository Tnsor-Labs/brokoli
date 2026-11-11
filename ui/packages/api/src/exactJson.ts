/*
 * JSON.parse that keeps integers exact.
 *
 * The API sends whole numbers past 2^53 exactly, and JSON.parse turns every
 * number into a double, so 9007199254740993 arrived as 9007199254740992 and
 * two different ids could look the same (#1001). This quotes each integer
 * literal that a double cannot hold before parsing, so it reaches the page as
 * its exact text. Every other number stays a number.
 *
 * A string rather than a bigint: the page shows values with String() and
 * nested ones with JSON.stringify, which throws on a bigint.
 */

const DIGITS_PAST_SAFE = /\d{16}/

export function parseJsonExact(text: string): unknown {
  // A safe integer has at most 16 digits, and 16-digit ones may still be
  // safe; without a run of 16 digits there is nothing to do.
  if (!DIGITS_PAST_SAFE.test(text)) return JSON.parse(text)
  return JSON.parse(quoteUnsafeIntegers(text))
}

function quoteUnsafeIntegers(text: string): string {
  let out = ''
  let copied = 0
  let i = 0
  while (i < text.length) {
    const c = text.charCodeAt(i)
    if (c === 0x22) {
      // Skip a string, escapes included.
      i++
      while (i < text.length) {
        const s = text.charCodeAt(i)
        if (s === 0x5c) i += 2
        else if (s === 0x22) break
        else i++
      }
      i++
      continue
    }
    if (c === 0x2d || (c >= 0x30 && c <= 0x39)) {
      const start = i
      i++
      while (i < text.length) {
        const d = text.charCodeAt(i)
        if (
          (d >= 0x30 && d <= 0x39) ||
          d === 0x2e ||
          d === 0x65 ||
          d === 0x45 ||
          d === 0x2b ||
          d === 0x2d
        )
          i++
        else break
      }
      const literal = text.slice(start, i)
      if (/^-?\d+$/.test(literal) && !Number.isSafeInteger(Number(literal))) {
        out += text.slice(copied, start) + '"' + literal + '"'
        copied = i
      }
      continue
    }
    i++
  }
  return copied === 0 ? text : out + text.slice(copied)
}
