package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// referenceInferColumnType is inferColumnType as it was before values were
// classified without always building their text: every value through %v,
// ParseInt, ParseFloat, ToLower and every date layout.
func referenceInferColumnType(col string, rows []common.DataRow) string {
	var intCount, floatCount, boolCount, dateCount, total int
	var wide bool
	for _, row := range rows {
		val, ok := row[col]
		if !ok || val == nil {
			continue
		}
		total++
		s := fmt.Sprintf("%v", val)
		if s == "" {
			continue
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			intCount++
			if n > math.MaxInt32 || n < math.MinInt32 {
				wide = true
			}
			continue
		}
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			floatCount++
			continue
		}
		lower := strings.ToLower(s)
		if lower == "true" || lower == "false" {
			boolCount++
			continue
		}
		if isDate(s) {
			dateCount++
		}
	}
	if total == 0 {
		return "TEXT"
	}
	threshold := int(float64(total) * 0.8)
	if threshold < 1 {
		threshold = 1
	}
	switch {
	case intCount >= threshold && wide:
		return "BIGINT"
	case intCount >= threshold:
		return "INTEGER"
	case floatCount+intCount >= threshold:
		return "FLOAT"
	case boolCount >= threshold:
		return "BOOLEAN"
	case dateCount >= threshold:
		return "TIMESTAMP"
	}
	return "TEXT"
}

var inferProbeValues = []interface{}{
	int64(0), int64(math.MaxInt32), int64(math.MaxInt32) + 1, int64(math.MinInt32) - 1, int64(math.MaxInt64), 7, -7,
	3.0, 1.5, math.Copysign(0, -1), 1e20, 1e21, math.MaxInt64, -math.MaxInt64, math.Exp2(63), -math.Exp2(63),
	float64(math.MaxInt32 + 1), 999999.0, -999999.0, 1e6, -1e6, 100000.0, 1200000.0, 999999.5, math.NaN(), math.Inf(1), math.Inf(-1), 5e-324,
	true, false, float32(2), float32(2.5), int32(5), uint64(math.MaxUint64), json.Number("12"), json.Number("1.5"),
	time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), []interface{}{1}, map[string]interface{}{"a": 1},
	"123", "-5", "+5", "2147483648", "9223372036854775808", "1.5", ".5", "5.", "-.5", "1e3", "1E-3",
	"inf", "+Inf", "-infinity", "Infinity", "NaN", "nan", "0x1p3", "0x10", "1_000", "0b101",
	"true", "TRUE", "False", "fAlSe", "yes", "truee", "ture", "t",
	"2026-01-02", "2026-01-02T10:00:00Z", "2026-01-02T10:00:00+02:00", "01/02/2006", "02-01-2006",
	"2026-01-02 15:04:05", "2026-13-02", "2026-1-2", "-006-01-02", "1/2/2006",
	"abc", "", " 5", "5 ", "true ", "-", "+", ".", "i", "n", "Inferno", "nancy", "+abc",
}

func TestInferColumnTypeAgreesWithTheTextClassifier(t *testing.T) {
	for _, v := range inferProbeValues {
		rows := []common.DataRow{{"c": v}}
		if got, want := inferColumnType("c", rows), referenceInferColumnType("c", rows); got != want {
			t.Errorf("%T(%v) alone: %s, the text classifier says %s", v, v, got, want)
		}
	}

	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 3000; trial++ {
		rows := make([]common.DataRow, 1+rng.Intn(12))
		for i := range rows {
			if rng.Intn(10) == 0 {
				rows[i] = common.DataRow{} // a missing value
				continue
			}
			rows[i] = common.DataRow{"c": inferProbeValues[rng.Intn(len(inferProbeValues))]}
		}
		if got, want := inferColumnType("c", rows), referenceInferColumnType("c", rows); got != want {
			var vals []string
			for _, r := range rows {
				vals = append(vals, fmt.Sprintf("%T(%v)", r["c"], r["c"]))
			}
			t.Fatalf("trial %d: %s, the text classifier says %s, for %v", trial, got, want, vals)
		}
	}
}
