package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestNodeRowCount(t *testing.T) {
	ds := func(n int) *common.DataSet {
		d := &common.DataSet{Columns: []string{"a"}}
		for i := 0; i < n; i++ {
			d.Rows = append(d.Rows, common.DataRow{"a": i})
		}
		return d
	}
	ref := func(n int64) *artifact.DatasetRef {
		return &artifact.DatasetRef{RowCount: n}
	}
	empty := &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}

	cases := []struct {
		name      string
		output    *common.DataSet
		outputRef *artifact.DatasetRef
		input     *common.DataSet
		inputRef  *artifact.DatasetRef
		want      int
	}{
		{"materialized output wins", ds(7), nil, ds(3), nil, 7},
		{"streamed output", nil, ref(200000), nil, nil, 200000},
		{"sink reports what it consumed", nil, nil, ds(42), nil, 42},
		// The regression: a streamed sink carries the reference it read
		// and the empty placeholder executeNode substitutes. Reading the
		// placeholder reported every streamed sink as having written
		// nothing.
		{"streamed sink prefers the ref over the placeholder", nil, nil, empty, ref(200000), 200000},
		{"nothing at all", nil, nil, nil, nil, 0},
	}
	for _, c := range cases {
		if got := nodeRowCount(c.output, c.outputRef, c.input, c.inputRef); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
