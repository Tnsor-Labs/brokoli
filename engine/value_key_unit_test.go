package engine

import "testing"

func TestValueKey(t *testing.T) {
	same := [][2]interface{}{
		{int64(1), float64(1)}, {int(7), int32(7)}, {uint8(3), int64(3)}, {float32(2.5), 2.5}, {"a", "a"}, {nil, nil}, {true, true},
	}
	for _, p := range same {
		if valueKey(p[0]) != valueKey(p[1]) {
			t.Errorf("valueKey(%#v) != valueKey(%#v)", p[0], p[1])
		}
	}
	different := [][2]interface{}{
		{int64(1), "1"}, {nil, "<nil>"}, {true, "true"}, {int64(1), true}, {1.5, "1.5"},
		{int64(9007199254740993), float64(9007199254740992)}, {"", nil},
	}
	for _, p := range different {
		if valueKey(p[0]) == valueKey(p[1]) {
			t.Errorf("valueKey(%#v) == valueKey(%#v)", p[0], p[1])
		}
	}
}
