package engine

import (
	"encoding/json"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// copyEscape and mysqlLoadEscape are the field encoders as strings, the
// form the tests that predate the append encoders assert on.
func copyEscape(v any) string      { return string(appendCopyField(nil, v)) }
func mysqlLoadEscape(v any) string { return string(appendMySQLLoadField(nil, v)) }

// The encoders as they were before they appended, kept verbatim as the
// reference the differential tests below hold the new ones to: the
// rewrite was for speed and must not change a byte.
func referenceCopyEscape(v any) string {
	if v == nil {
		return `\N`
	}
	var s string
	switch t := v.(type) {
	case time.Time:
		s = t.Format("2006-01-02 15:04:05.999999-07:00")
	case *time.Time:
		if t == nil {
			return `\N`
		}
		s = t.Format("2006-01-02 15:04:05.999999-07:00")
	case string:
		s = t
	case []byte:
		s = string(t)
	case float64:
		// Never %v a float here. Go renders float64(1000000) as "1e+06",
		// which Postgres rejects for an integer column and, worse, accepts
		// for a text one (#334). Decoding normalises integers to int64 so
		// this should rarely be reached, but a renderer that can silently
		// change a value is not something to leave in place on the strength
		// of "should".
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			s = strconv.FormatFloat(t, 'f', -1, 64)
		} else {
			s = strconv.FormatFloat(t, 'g', -1, 64)
		}
	default:
		s = valueText(t)
	}

	if !strings.ContainsAny(s, "\\\t\n\r") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)
	return r.Replace(s)
}

func referenceMySQLLoadEscape(v any) string {
	if v == nil {
		return `\N`
	}
	var s string
	switch t := v.(type) {
	case time.Time:
		s = t.UTC().Format("2006-01-02 15:04:05.999999")
	case *time.Time:
		if t == nil {
			return `\N`
		}
		s = t.UTC().Format("2006-01-02 15:04:05.999999")
	case bool:
		if t {
			return "1"
		}
		return "0"
	case json.Number:
		s = t.String()
	case []byte:
		s = string(t)
	case string:
		s = t
	default:
		s = valueText(t)
	}
	if !strings.ContainsAny(s, "\\\t\n\r\x00") {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			sb.WriteString(`\\`)
		case '\t':
			sb.WriteString(`\t`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case 0:
			sb.WriteString(`\0`)
		default:
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}

// encoderPool is a value of every kind a source can hand a bulk writer,
// with the edges each rendering has: the escape characters alone and
// together, NUL, integer extremes, floats either side of the 1e15 switch
// and the ones %v renders specially, times with odd offsets and years
// outside four digits, typed nils and nested values.
func encoderPool(rng *rand.Rand) []any {
	lmt := time.FixedZone("LMT", -(4*3600 + 56*60 + 2)) // an offset with seconds
	east := time.FixedZone("", 5*3600+45*60)
	pool := []any{
		nil, "", "plain", `\`, "\t", "\n", "\r", "\x00", `a\b`, "a\tb\nc\rd\\e\x00f",
		"\\\\\t\t", "quote's \"here\"", "naïve ünïcode ✓", strings.Repeat("x\n", 50),
		[]byte(nil), []byte("bytes\twith\ttabs"), []byte{0, '\\', 'z'},
		int64(0), int64(-1), int64(math.MaxInt64), int64(math.MinInt64), int64(1e6),
		0, -42, math.MaxInt, int8(-8), int32(7), uint64(math.MaxUint64), uint8(200),
		true, false,
		0.0, math.Copysign(0, -1), 1.5, -2.25, 1e6, 1e15 - 1, 1e15, 1e21, 1e-7, 123456789.125,
		math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64, math.SmallestNonzeroFloat64,
		float32(1.1), float32(1e7),
		json.Number("12"), json.Number("1e3"), json.Number("x\ty"),
		time.Time{},
		time.Date(2026, 10, 30, 12, 34, 56, 0, time.UTC),
		time.Date(2026, 10, 30, 12, 34, 56, 789000000, time.UTC),
		time.Date(2026, 10, 30, 12, 34, 56, 123456789, east),
		time.Date(1883, 11, 18, 12, 0, 0, 1000, lmt),
		time.Date(-44, 3, 15, 0, 0, 0, 0, time.UTC),
		time.Date(12026, 1, 1, 0, 0, 0, 0, time.UTC),
		(*time.Time)(nil),
		(*int)(nil),
		map[string]any{"a": 1, "b": []any{"<x>", 2.5}},
		[]any{"tab\there", nil},
		[2]int{1, 2},
		struct{ A int }{3},
	}
	ts := time.Date(2001, 2, 3, 4, 5, 6, 7, time.UTC)
	pool = append(pool, &ts)
	zones := []*time.Location{time.UTC, lmt, east, time.FixedZone("", -11*3600)}
	alphabet := []byte("ab \\\t\n\r\x00é")
	for range 2000 {
		switch rng.Intn(5) {
		case 0:
			pool = append(pool, rng.Int63()-rng.Int63())
		case 1:
			pool = append(pool, (rng.Float64()-0.5)*math.Pow(10, float64(rng.Intn(40)-10)))
		case 2:
			pool = append(pool, float64(rng.Int63n(1<<53)-(1<<52))*math.Pow(10, float64(rng.Intn(6))))
		case 3:
			pool = append(pool, time.Unix(rng.Int63n(1<<36)-(1<<35), rng.Int63n(1e9)).In(zones[rng.Intn(len(zones))]))
		default:
			b := make([]byte, rng.Intn(12))
			for i := range b {
				b[i] = alphabet[rng.Intn(len(alphabet))]
			}
			pool = append(pool, string(b))
		}
	}
	return pool
}

func TestAppendCopyFieldMatchesTheStringEncoder(t *testing.T) {
	for i, v := range encoderPool(rand.New(rand.NewSource(1))) {
		want := referenceCopyEscape(v)
		if got := copyEscape(v); got != want {
			t.Errorf("value %d (%T %#v): got %q, want %q", i, v, v, got, want)
		}
		// Appending must extend dst, never overwrite what is already there.
		if got := string(appendCopyField([]byte("prefix|"), v)); got != "prefix|"+want {
			t.Errorf("value %d (%T): appended to a prefix: got %q", i, v, got)
		}
	}
}

func TestAppendMySQLLoadFieldMatchesTheStringEncoder(t *testing.T) {
	for i, v := range encoderPool(rand.New(rand.NewSource(2))) {
		want := referenceMySQLLoadEscape(v)
		if got := mysqlLoadEscape(v); got != want {
			t.Errorf("value %d (%T %#v): got %q, want %q", i, v, v, got, want)
		}
		if got := string(appendMySQLLoadField([]byte("prefix|"), v)); got != "prefix|"+want {
			t.Errorf("value %d (%T): appended to a prefix: got %q", i, v, got)
		}
	}
}

// The readers reuse one line buffer across rows; a row must never carry
// bytes from the longer row before it.
func TestTextLinesDoNotCarryBytesBetweenRows(t *testing.T) {
	cols := []string{"a", "b"}
	rows := []common.DataRow{
		{"a": "a long first value\twith a tab", "b": int64(123456789)},
		{"a": "x", "b": nil},
		{"a": true, "b": 2.5},
	}
	for name, enc := range map[string]struct {
		field func([]byte, any) []byte
		ref   func(any) string
	}{
		"copy":  {appendCopyField, referenceCopyEscape},
		"mysql": {appendMySQLLoadField, referenceMySQLLoadEscape},
	} {
		var line []byte
		for i, row := range rows {
			line = appendTextLine(line[:0], row, cols, enc.field)
			want := enc.ref(row["a"]) + "\t" + enc.ref(row["b"]) + "\n"
			if string(line) != want {
				t.Errorf("%s row %d: got %q, want %q", name, i, line, want)
			}
		}
	}
}
