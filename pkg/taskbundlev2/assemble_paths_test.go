package taskbundlev2

// Pins how a path is written in a bundle's tar headers
// (task-bundle-v2-canonicalization.md, "How a path is written"): in the
// name field when it fits, split into prefix and name when it splits, and
// otherwise behind a PAX extended header. A second builder has to write the
// same bytes, so this reads the raw 512-byte headers rather than going
// through archive/tar's reader, which would hide them.

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
)

type rawHeader struct {
	name, prefix, mode, uid, gid, mtime, devmajor, devminor string
	typeflag                                                byte
	data                                                    string
}

func rawHeaders(t *testing.T, archive []byte) []rawHeader {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tarBytes, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	field := func(b []byte) string { return string(bytes.TrimRight(b, "\x00")) }
	var out []rawHeader
	for off := 0; off+512 <= len(tarBytes); {
		h := tarBytes[off : off+512]
		if bytes.Equal(h, make([]byte, 512)) {
			break
		}
		if string(h[257:263]) != "ustar\x00" || string(h[263:265]) != "00" {
			t.Fatalf("header at %d: magic %q version %q, want ustar\\0 00", off, h[257:263], h[263:265])
		}
		size, err := strconv.ParseInt(strings.TrimRight(string(h[124:136]), "\x00 "), 8, 64)
		if err != nil {
			t.Fatal(err)
		}
		r := rawHeader{name: field(h[0:100]), prefix: field(h[345:500]), mode: string(h[100:108]), uid: string(h[108:116]),
			gid: string(h[116:124]), mtime: string(h[136:148]), devmajor: string(h[329:337]), devminor: string(h[337:345]), typeflag: h[156]}
		if r.typeflag == 'x' {
			r.data = string(tarBytes[off+512 : off+512+int(size)])
		}
		out = append(out, r)
		off += 512 + int((size+511)/512)*512
	}
	return out
}

func TestAssembleWritesEachPathAsTheSpecSays(t *testing.T) {
	deep := "com/example/logistics/warehouse/inbound/scheduling/dockassignment/priorityqueues/Deliveries_BrokoliTasks.class"
	longBase := "com/example/pax/Docks$" + strings.Repeat("Window", 15) + ".class"
	unicode := "com/example/ladunki/Magazyn$Ładunek.class"
	archive, err := Assemble(map[string]string{deep: "d", longBase: "l", unicode: "u"}, everyFieldManifest())
	if err != nil {
		t.Fatal(err)
	}
	headers := rawHeaders(t, archive)
	byName := func(name string) (int, rawHeader) {
		for i, h := range headers {
			if h.name == name {
				return i, h
			}
		}
		t.Fatalf("no header named %q in %v", name, headers)
		return 0, rawHeader{}
	}

	// It splits: prefix and name, no PAX header.
	_, h := byName("Deliveries_BrokoliTasks.class")
	if h.prefix != "com/example/logistics/warehouse/inbound/scheduling/dockassignment/priorityqueues" || h.typeflag != '0' {
		t.Errorf("split path: prefix %q type %c", h.prefix, h.typeflag)
	}

	// Otherwise a PAX header: an x entry named dir/PaxHeaders.0/base,
	// ASCII-only and cut to 100 bytes, with one self-counting path record,
	// zero mode/ids/mtime and NUL device fields; then the entry's own
	// header with the same 100-byte ASCII name.
	for _, full := range []string{longBase, unicode} {
		dir, base := full[:strings.LastIndex(full, "/")+1], full[strings.LastIndex(full, "/")+1:]
		ascii := strings.Map(func(r rune) rune {
			if r >= 0x80 || r == 0 {
				return -1
			}
			return r
		}, dir+"PaxHeaders.0/"+base)
		if len(ascii) > 100 {
			ascii = ascii[:100]
		}
		i, x := byName(ascii)
		if x.typeflag != 'x' {
			t.Fatalf("%s: header %q is type %c, want x", full, ascii, x.typeflag)
		}
		record := " path=" + full + "\n"
		n := len(record) + len(strconv.Itoa(len(record)))
		if len(strconv.Itoa(n)) != len(strconv.Itoa(len(record))) {
			n++
		}
		if want := fmt.Sprintf("%d%s", n, record); x.data != want {
			t.Errorf("%s: PAX data %q, want %q", full, x.data, want)
		}
		if x.mode != "0000000\x00" || x.uid != "0000000\x00" || x.gid != "0000000\x00" || x.mtime != "00000000000\x00" {
			t.Errorf("%s: PAX mode %q uid %q gid %q mtime %q, want zeros", full, x.mode, x.uid, x.gid, x.mtime)
		}
		if x.devmajor != strings.Repeat("\x00", 8) || x.devminor != strings.Repeat("\x00", 8) {
			t.Errorf("%s: PAX devmajor %q devminor %q, want NUL-filled", full, x.devmajor, x.devminor)
		}
		entry := headers[i+1]
		wantName := strings.Map(func(r rune) rune {
			if r >= 0x80 || r == 0 {
				return -1
			}
			return r
		}, full)
		if len(wantName) > 100 {
			wantName = wantName[:100]
		}
		if entry.name != wantName || entry.prefix != "" || entry.typeflag != '0' || entry.devmajor != "0000000\x00" {
			t.Errorf("%s: entry header name %q prefix %q type %c devmajor %q", full, entry.name, entry.prefix, entry.typeflag, entry.devmajor)
		}
	}

	// It fits: a short ASCII path in the name field alone.
	if _, h := byName("manifest.json"); h.prefix != "" || h.typeflag != '0' {
		t.Errorf("manifest.json: prefix %q type %c", h.prefix, h.typeflag)
	}
}
