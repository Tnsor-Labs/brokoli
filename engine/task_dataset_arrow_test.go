package engine

// ADR-033 section 8: "Transport choice is physical-plan metadata and
// does not change the logical dataset contract." These tests exist to
// hold that line -- the codec may change how bytes travel, never what
// the engine hands downstream.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func stageBytes(t *testing.T, name string, content []byte) (dir string, size int64, checksum string) {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return d, int64(len(content)), "sha256:" + hex.EncodeToString(sum[:])
}

// The property the whole codec choice rests on: identical data over
// either transport must yield an identical DataSet. If this ever fails,
// downstream consumers would have to know which transport was picked,
// which is exactly what section 8 forbids.
func TestArrowAndNDJSONProduceIdenticalDataSets(t *testing.T) {
	columns := []string{"id", "name"}
	rows := []common.DataRow{
		{"id": "1", "name": "alpha"},
		{"id": "2", "name": "beta"},
	}

	arrowBytes, err := arrowIPCFromRows(columns, rows)
	if err != nil {
		t.Fatalf("encode arrow: %v", err)
	}
	aDir, aSize, aSum := stageBytes(t, "result.arrow", arrowBytes)
	viaArrow, err := readTaskDatasetOutput(aDir, "result.arrow", CodecArrowIPC, aSize, aSum)
	if err != nil {
		t.Fatalf("read arrow: %v", err)
	}

	var nd bytes.Buffer
	for _, r := range rows {
		nd.WriteString(fmt.Sprintf(`{"id":%q,"name":%q}`+"\n", r["id"], r["name"]))
	}
	nDir, nSize, nSum := stageBytes(t, "result.ndjson", nd.Bytes())
	viaNDJSON, err := readTaskDatasetOutput(nDir, "result.ndjson", CodecNDJSON, nSize, nSum)
	if err != nil {
		t.Fatalf("read ndjson: %v", err)
	}

	if strings.Join(viaArrow.Columns, ",") != strings.Join(viaNDJSON.Columns, ",") {
		t.Errorf("columns differ by transport: arrow=%v ndjson=%v", viaArrow.Columns, viaNDJSON.Columns)
	}
	if len(viaArrow.Rows) != len(viaNDJSON.Rows) {
		t.Fatalf("row counts differ: arrow=%d ndjson=%d", len(viaArrow.Rows), len(viaNDJSON.Rows))
	}
	for i := range viaArrow.Rows {
		for _, c := range columns {
			a, n := viaArrow.Rows[i][c], viaNDJSON.Rows[i][c]
			if a != n {
				t.Errorf("row %d column %q differs by transport: arrow=%#v ndjson=%#v", i, c, a, n)
			}
		}
	}
}

// The integrity guarantee must not weaken for the new codec: Arrow's
// reader stops at the stream's end-of-stream marker rather than at EOF,
// so a naive implementation would hash only what it parsed and happily
// accept trailing bytes appended after it.
func TestArrowChecksumCoversTrailingBytes(t *testing.T) {
	arrowBytes, err := arrowIPCFromRows([]string{"n"}, []common.DataRow{{"n": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	tampered := append(append([]byte{}, arrowBytes...), []byte("extra smuggled bytes")...)

	// The manifest declares the size and checksum of the TAMPERED file,
	// so size and length checks pass; only hashing every byte actually
	// present can catch this.
	dir, size, sum := stageBytes(t, "result.arrow", tampered)
	honest := sha256.Sum256(arrowBytes)
	if sum == "sha256:"+hex.EncodeToString(honest[:]) {
		t.Fatal("setup: tampered file hashes the same as the clean one")
	}
	if _, err := readTaskDatasetOutput(dir, "result.arrow", CodecArrowIPC, size, sum); err != nil {
		t.Fatalf("a self-consistent file was rejected: %v", err)
	}

	// And the reverse: a file whose declared checksum covers only the
	// arrow stream, with bytes appended after, must be refused.
	dir2, _, _ := stageBytes(t, "result.arrow", tampered)
	_, err = readTaskDatasetOutput(dir2, "result.arrow", CodecArrowIPC,
		int64(len(tampered)), "sha256:"+hex.EncodeToString(honest[:]))
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("err = %v, want an integrity failure when the checksum covers only the parsed prefix", err)
	}
}

func TestArrowMalformedStreamIsRefused(t *testing.T) {
	dir, size, sum := stageBytes(t, "result.arrow", []byte("this is definitely not arrow ipc"))
	_, err := readTaskDatasetOutput(dir, "result.arrow", CodecArrowIPC, size, sum)
	if err == nil || !strings.Contains(err.Error(), "arrow") {
		t.Fatalf("err = %v, want a named arrow decode failure", err)
	}
}

// A codec neither side implements is still refused by name, and the
// message names both that this server does support.
func TestUnknownCodecNamesWhatIsSupported(t *testing.T) {
	dir, size, sum := stageBytes(t, "result.parquet", []byte("PAR1"))
	_, err := readTaskDatasetOutput(dir, "result.parquet", "parquet/v1", size, sum)
	if err == nil {
		t.Fatal("an unsupported codec was accepted")
	}
	for _, want := range []string{"parquet/v1", CodecNDJSON, CodecArrowIPC} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}
