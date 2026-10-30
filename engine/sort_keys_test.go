package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// referenceSort is the sort as it was before numbers had a float64 form:
// every value read from its %v text, every number a 200-bit big.Float.
func referenceSort(rows []common.DataRow, col string, ascending bool) []common.DataRow {
	type key struct {
		class int
		num   *big.Float
		text  string
	}
	keyOf := func(v interface{}) key {
		if v == nil {
			return key{class: sortClassNull}
		}
		s := fmt.Sprintf("%v", v)
		if f, _, err := big.ParseFloat(s, 10, sortKeyPrecision, big.ToNearestEven); err == nil {
			return key{class: sortClassNumber, num: f}
		}
		return key{class: sortClassText, text: s}
	}
	cmp := func(a, b key) int {
		if a.class != b.class {
			if a.class < b.class {
				return -1
			}
			return 1
		}
		switch a.class {
		case sortClassNumber:
			return a.num.Cmp(b.num)
		case sortClassText:
			return strings.Compare(a.text, b.text)
		}
		return 0
	}
	out := append([]common.DataRow(nil), rows...)
	keys := make(map[int]key, len(out))
	for i, r := range out {
		r["_i"] = i
		keys[i] = keyOf(r[col])
	}
	sort.SliceStable(out, func(a, b int) bool {
		c := cmp(keys[out[a]["_i"].(int)], keys[out[b]["_i"].(int)])
		if ascending {
			return c < 0
		}
		return c > 0
	})
	return out
}

func TestSortOrdersExactlyAsTheDecimalComparison(t *testing.T) {
	fast := []interface{}{
		0.1, 0.2, 0.30000000000000004, 1e21, 1e-7, 12.5, math.Copysign(0, -1), 0.0,
		int64(-3), int64(9007199254740991), int64(1 << 52), 7, -7,
		"0.1", "12.5", "100", "-3", "1e+21", "007", "+5", "-0",
		"12.50", "0.10", "1234567", "5.", ".5", "-0.0", "000.1", "123456789012345", "1234567890123456",
	}
	slow := []interface{}{
		"0.10000000000000000001", "0.3000000000000001", "1e3", "9007199254740993", "1e-320",
		int64(9007199254740993), int64(math.MaxInt64), int64(math.MinInt64), float32(0.1),
		json.Number("0.1000"), math.Inf(1), math.Inf(-1), "Inf", "-inf", "+Inf",
	}
	text := []interface{}{math.NaN(), "NaN", "abc", "", "Infinity", "0x10", "1_000", " 5", "5 ", "-", ".", nil, true}

	// Values packed where rounding is tight: floats, their neighbours, and
	// decimals of every length around them, some with a trailing zero.
	var near []interface{}
	for _, b := range []float64{0.1, 0.3, 1.0 / 3, 123456.789, 1e15 / 3, 9007199254740990, 2.5e-300} {
		for _, f := range []float64{b, math.Nextafter(b, 0), math.Nextafter(b, math.Inf(1))} {
			near = append(near, f)
			for digits := 1; digits <= 17; digits++ {
				d := strconv.FormatFloat(f, 'g', digits, 64)
				near = append(near, d)
				if !strings.ContainsAny(d, "e") {
					if !strings.Contains(d, ".") {
						d += "."
					}
					near = append(near, d+"0")
				}
			}
		}
	}

	// Values the exact form must keep: distinct decimals that round to one
	// float64. 16 significant digits near 9, where a float64 is coarser
	// than the decimals, and plain decimals among the subnormals. A column
	// of these and fast values alone is where a too-generous float64 form
	// shows, since any other exact-only value would upgrade the column.
	var tight []interface{}
	for k := 1; k <= 6; k++ {
		tight = append(tight, fmt.Sprintf("9.00000000000000%d", k), fmt.Sprintf("8.50000000000000%d", k))
	}
	for _, d := range []string{"5", "6", "7", "9"} {
		tight = append(tight, "0."+strings.Repeat("0", 323)+d)
	}

	rng := rand.New(rand.NewSource(1))
	pick := func(pool []interface{}, n int) []interface{} {
		out := make([]interface{}, n)
		for i := range out {
			out[i] = pool[rng.Intn(len(pool))]
		}
		return out
	}
	for trial := 0; trial < 2000; trial++ {
		var values []interface{}
		switch trial % 3 {
		case 0:
			values = append(pick(fast, 6), pick(near, 10)...)
		case 1: // an exact-only number in the column
			values = append(pick(fast, 12), pick(slow, 3)...)
		case 2:
			values = append(pick(fast, 6), pick(tight, 8)...)
		}
		values = append(values, pick(text, 3)...)
		rng.Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })

		for _, asc := range []bool{true, false} {
			rows := make([]common.DataRow, len(values))
			for i, v := range values {
				rows[i] = common.DataRow{"k": v, "_i": i}
			}
			want := referenceSort(rows, "k", asc)
			ds := &common.DataSet{Columns: []string{"k"}, Rows: append([]common.DataRow(nil), rows...)}
			if err := sortRows(TransformRule{Type: "sort", Columns: []string{"k"}, Ascending: asc}, ds); err != nil {
				t.Fatal(err)
			}
			for i := range want {
				if ds.Rows[i]["_i"] != want[i]["_i"] {
					got, exp := make([]string, len(ds.Rows)), make([]string, len(want))
					for j := range want {
						got[j] = fmt.Sprintf("%T(%v)", ds.Rows[j]["k"], ds.Rows[j]["k"])
						exp[j] = fmt.Sprintf("%T(%v)", want[j]["k"], want[j]["k"])
					}
					t.Fatalf("trial %d asc=%v: order differs at %d\n got  %v\n want %v", trial, asc, i, got, exp)
				}
			}
		}
	}
}
