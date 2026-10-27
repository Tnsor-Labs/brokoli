package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// valueKey is the identity of one value for dedup, join and count_distinct:
// two values have the same key exactly when those operations should treat
// them as the same value.
//
// Numbers compare by value, whatever Go type carries them, because the same
// column arrives as int64 from a database and as float64 from other
// sources, and 5 must join 5.0. Text never equals a number ("1" is not 1),
// nil is not the text "<nil>", and a boolean is not its spelling. Each
// kind has its own prefix, so no value of one kind can produce another
// kind's key.
func valueKey(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "z"
	case string:
		return "s:" + x
	case bool:
		if x {
			return "b:1"
		}
		return "b:0"
	case int:
		return "n:" + strconv.FormatInt(int64(x), 10)
	case int8:
		return "n:" + strconv.FormatInt(int64(x), 10)
	case int16:
		return "n:" + strconv.FormatInt(int64(x), 10)
	case int32:
		return "n:" + strconv.FormatInt(int64(x), 10)
	case int64:
		return "n:" + strconv.FormatInt(x, 10)
	case uint:
		return "n:" + strconv.FormatUint(uint64(x), 10)
	case uint8:
		return "n:" + strconv.FormatUint(uint64(x), 10)
	case uint16:
		return "n:" + strconv.FormatUint(uint64(x), 10)
	case uint32:
		return "n:" + strconv.FormatUint(uint64(x), 10)
	case uint64:
		return "n:" + strconv.FormatUint(x, 10)
	case float32:
		return floatKey(float64(x))
	case float64:
		return floatKey(x)
	default:
		return fmt.Sprintf("%T:%#v", v, v)
	}
}

// floatKey gives a whole float inside int64's range the integer's key, so
// 5.0 and 5 agree; any other float keeps its shortest exact spelling.
func floatKey(f float64) string {
	if f == math.Trunc(f) && f >= -(1<<63) && f < 1<<63 {
		return "n:" + strconv.FormatInt(int64(f), 10)
	}
	return "f:" + strconv.FormatFloat(f, 'g', -1, 64)
}

// valuesKey is the identity of several values in order. Each part carries
// its length, so a separator inside one value cannot make two different
// rows line up ("x\x00","y" and "x","\x00y" used to).
func valuesKey(values []interface{}) string {
	var b strings.Builder
	for _, v := range values {
		k := valueKey(v)
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
	}
	return b.String()
}
