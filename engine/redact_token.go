package engine

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// replaceSecretToken replaces each occurrence of secret in msg with
// replacement, but only where it stands as a token of its own: an edge of
// the secret that is a letter or digit must not be glued to another letter
// or digit in msg. A plain replace-all could not tell a one-letter password
// "p" from the p in "supported", so it mangled the very message an operator
// needed (#780). Requiring a token boundary still removes the secret
// wherever it appears on its own, at any length: in "password p rejected",
// after "=", between quotes, at either end of the message. It is never
// weakened by length, because a short password left in a run log is worse
// than an over-redacted message.
func replaceSecretToken(msg, secret, replacement string) string {
	if secret == "" || !strings.Contains(msg, secret) {
		return msg
	}
	first, _ := utf8.DecodeRuneInString(secret)
	last, _ := utf8.DecodeLastRuneInString(secret)
	guardBefore, guardAfter := isWordRune(first), isWordRune(last)

	var b strings.Builder
	start := 0
	for i := 0; i < len(msg); {
		j := strings.Index(msg[i:], secret)
		if j < 0 {
			break
		}
		j += i
		end := j + len(secret)
		before, _ := utf8.DecodeLastRuneInString(msg[:j])
		after, _ := utf8.DecodeRuneInString(msg[end:])
		glued := (guardBefore && j > 0 && isWordRune(before)) || (guardAfter && end < len(msg) && isWordRune(after))
		if glued {
			// Not this occurrence; look again from the next rune, so an
			// occurrence overlapping this one is still found.
			_, size := utf8.DecodeRuneInString(msg[j:])
			i = j + size
			continue
		}
		b.WriteString(msg[start:j])
		b.WriteString(replacement)
		start, i = end, end
	}
	if start == 0 {
		return msg
	}
	b.WriteString(msg[start:])
	return b.String()
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
