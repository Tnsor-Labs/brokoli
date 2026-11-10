package taskinterface

// The semantic digest of task-interface-canonicalization.md section 5: a
// SHA-256 over a document's canonical rendering, which is the rendering of
// ir-canonicalization.md section 2 (two-space indent, keys in code point
// order, numbers by RFC 8785, one trailing newline) after every record
// descriptor's fields are sorted by name. Every SDK computes the same bytes;
// the cross-SDK vectors in docs/schema/fixtures/task-interface/differential
// carry the expected rendering and digest of each document.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// UnknownRowsInterface is the interface a task node has when it declares
// none: one input port and one result port, each a dataset of unknown
// rows. A task whose inferred interface is null (it takes and returns rows
// whose schema it does not declare) hashes this document, so the node and
// the bundle that implements it agree on one digest.
func UnknownRowsInterface() map[string]interface{} {
	port := func() map[string]interface{} {
		return map[string]interface{}{"value": map[string]interface{}{
			"kind": "dataset",
			"row":  map[string]interface{}{"kind": "unknown"},
		}}
	}
	return map[string]interface{}{
		"contract": "brokoli.task-interface/v1",
		"inputs":   map[string]interface{}{"input": port()},
		"outputs":  map[string]interface{}{"result": port()},
	}
}

// InterfaceDigest is the semantic digest of a task interface, the value a
// task bundle's interface_digest holds. A nil interface digests as
// UnknownRowsInterface.
func InterfaceDigest(iface interface{}) (string, error) {
	if iface == nil {
		iface = UnknownRowsInterface()
	}
	return Digest(iface)
}

// Digest is the semantic digest of a document: a task interface, a
// pipeline's parameter declarations, or a portable value.
func Digest(doc interface{}) (string, error) {
	rendered, err := CanonicalRendering(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(rendered)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CanonicalRendering is the exact text a digest hashes: doc with every
// record descriptor's fields sorted by name in UTF-8 byte order, rendered
// by ir-canonicalization.md section 2. doc is decoded JSON: maps, slices,
// strings, booleans, nil, and numbers as float64, int64, int or
// json.Number.
func CanonicalRendering(doc interface{}) ([]byte, error) {
	var b bytes.Buffer
	if err := renderCanonical(&b, sortRecordFields(doc), 0); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

// sortRecordFields returns a copy of v in which every object of kind
// "record" has its fields list sorted by name. Declaration order is never
// semantic for a record (section 4 rule 2); every other list keeps its
// order, an enum's values included.
func sortRecordFields(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, child := range x {
			out[k] = sortRecordFields(child)
		}
		if out["kind"] == "record" {
			if fields, ok := out["fields"].([]interface{}); ok {
				sorted := append([]interface{}(nil), fields...)
				sort.SliceStable(sorted, func(i, j int) bool { return fieldName(sorted[i]) < fieldName(sorted[j]) })
				out["fields"] = sorted
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, child := range x {
			out[i] = sortRecordFields(child)
		}
		return out
	}
	return v
}

func fieldName(v interface{}) string {
	if m, ok := v.(map[string]interface{}); ok {
		name, _ := m["name"].(string)
		return name
	}
	return ""
}

const canonicalIndent = "  "

func renderCanonical(b *bytes.Buffer, v interface{}, level int) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		return writeCanonicalString(b, x)
	case float64:
		text, err := canonicalNumber(x)
		if err != nil {
			return err
		}
		b.WriteString(text)
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case json.Number:
		if i, err := x.Int64(); err == nil {
			b.WriteString(strconv.FormatInt(i, 10))
			return nil
		}
		f, err := x.Float64()
		if err != nil {
			return fmt.Errorf("canonical rendering: %q is not a number", x.String())
		}
		text, err := canonicalNumber(f)
		if err != nil {
			return err
		}
		b.WriteString(text)
	case []interface{}:
		if len(x) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		for i, child := range x {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(strings.Repeat(canonicalIndent, level+1))
			if err := renderCanonical(b, child, level+1); err != nil {
				return err
			}
		}
		b.WriteString("\n" + strings.Repeat(canonicalIndent, level) + "]")
	case map[string]interface{}:
		if len(x) == 0 {
			b.WriteString("{}")
			return nil
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		// Byte order of UTF-8 is code point order.
		sort.Strings(keys)
		b.WriteString("{\n")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(strings.Repeat(canonicalIndent, level+1))
			if err := writeCanonicalString(b, k); err != nil {
				return err
			}
			b.WriteString(": ")
			if err := renderCanonical(b, x[k], level+1); err != nil {
				return err
			}
		}
		b.WriteString("\n" + strings.Repeat(canonicalIndent, level) + "}")
	default:
		return fmt.Errorf("canonical rendering: %T has no JSON form", v)
	}
	return nil
}

// writeCanonicalString writes s as JSON.stringify and Python's
// json.dumps(ensure_ascii=False) both write it: the two-character escapes
// for '"', '\\', backspace, form feed, newline, carriage return and tab, a
// six-character escape with lowercase hex for every other character below
// U+0020, and everything else literal. Not Go's encoding/json, which also
// escapes '<', '>', '&', U+2028 and U+2029.
func writeCanonicalString(b *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("canonical rendering: %q is not valid UTF-8", s)
	}
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, "%cu%04x", 92, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return nil
}

// canonicalNumber writes a float64 as RFC 8785 section 3.2.2.3 does: the
// shortest digits that read back as the same float64, laid out by
// ECMAScript's Number-to-string rule. -0 is 0; NaN and the infinities are
// refused.
func canonicalNumber(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("canonical rendering: %v is not a portable number", f)
	}
	if f == 0 {
		return "0", nil
	}
	if f < 0 {
		text, err := canonicalNumber(-f)
		return "-" + text, err
	}
	// 'e' with precision -1 is the shortest representation that reads back,
	// as d.ddde±XX.
	mantissa, exp, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	e, err := strconv.Atoi(exp)
	if err != nil {
		return "", err
	}
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		digits = "0"
	}
	count, point := len(digits), e+1
	switch {
	case count <= point && point <= 21:
		return digits + strings.Repeat("0", point-count), nil
	case 0 < point && point <= 21:
		return digits[:point] + "." + digits[point:], nil
	case -6 < point && point <= 0:
		return "0." + strings.Repeat("0", -point) + digits, nil
	}
	head := digits
	if count > 1 {
		head = digits[:1] + "." + digits[1:]
	}
	sign := "+"
	if point-1 < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%se%s%d", head, sign, abs(point-1)), nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
