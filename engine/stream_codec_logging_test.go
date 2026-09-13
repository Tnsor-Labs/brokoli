package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
)

// The codec was invisible: two of them, decided per stream from the
// first batch, and nothing said which ran. A silent fallback to NDJSON
// is by design, so it is exactly the thing an operator must be able to
// see rather than infer from a stopwatch.

func TestDatasetRefFormatName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  *artifact.DatasetRef
		want string
	}{
		{"arrow", &artifact.DatasetRef{Format: artifact.FormatArrowIPC}, artifact.FormatArrowIPC},
		{"ndjson", &artifact.DatasetRef{Format: artifact.FormatNDJSON}, artifact.FormatNDJSON},
		// An empty Format means NDJSON, which is what every reader
		// already treats it as; reporting "" would read as a bug.
		{"empty means ndjson", &artifact.DatasetRef{}, artifact.FormatNDJSON},
		{"nil means ndjson", nil, artifact.FormatNDJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := datasetRefFormatName(tc.ref); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The name has to be the same string the ref carries and the same one
// BROKOLI_STREAM_CODEC accepts, or an operator reading the log cannot
// act on what they read.
func TestLoggedCodecNameMatchesTheSettingThatSelectsIt(t *testing.T) {
	for _, name := range []string{
		datasetRefFormatName(&artifact.DatasetRef{Format: artifact.FormatArrowIPC}),
		datasetRefFormatName(&artifact.DatasetRef{Format: artifact.FormatNDJSON}),
	} {
		t.Setenv(streamCodecEnv, name)
		got := streamCodecFromEnv()
		if got == streamCodecAuto {
			t.Errorf("%s=%q selects auto, so the name in the log is not one the setting accepts",
				streamCodecEnv, name)
		}
		if strings.TrimSpace(name) == "" {
			t.Errorf("empty codec name in a log line")
		}
	}
}
