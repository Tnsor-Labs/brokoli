package drivers

import (
	"errors"
	"strings"
	"testing"
)

func TestManagerRequiredCapabilities(t *testing.T) {
	dir := t.TempDir()
	archive, archiveDigest := testArchive(t, "adbc-postgresql", "driver.so", []byte("native library"), nil)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := mgr.InstallArchive(archive, archiveDigest)
	if err != nil {
		t.Fatal(err)
	}

	identity := DriverIdentity{Name: installed.Name, Version: installed.Version, LibrarySHA256: strings.ToUpper(installed.LibrarySHA256)}
	got, err := mgr.RequiredCapabilities(identity)
	if err != nil {
		t.Fatalf("RequiredCapabilities: %v", err)
	}
	want := []string{
		"native-adbc",
		"native-adbc:adbc-postgresql",
		"native-adbc:adbc-postgresql:1.0.0:" + installed.LibrarySHA256[:libraryDigestPrefixLength],
	}
	if len(got) != len(want) {
		t.Fatalf("RequiredCapabilities() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RequiredCapabilities() = %q, want %q", got, want)
		}
	}
}

func TestManagerRequiredCapabilitiesFailsClosed(t *testing.T) {
	dir := t.TempDir()
	archive, archiveDigest := testArchive(t, "flightsql", "driver.so", []byte("native library"), nil)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := mgr.InstallArchive(archive, archiveDigest)
	if err != nil {
		t.Fatal(err)
	}
	valid := DriverIdentity{Name: installed.Name, Version: installed.Version, LibrarySHA256: installed.LibrarySHA256}

	tests := []struct {
		name string
		id   DriverIdentity
		want error
	}{
		{name: "missing driver", id: DriverIdentity{Name: "missing", Version: valid.Version, LibrarySHA256: valid.LibrarySHA256}, want: ErrDriverNotInstalled},
		{name: "wrong version", id: DriverIdentity{Name: valid.Name, Version: "2.0.0", LibrarySHA256: valid.LibrarySHA256}, want: ErrDriverIdentityMismatch},
		{name: "wrong digest", id: DriverIdentity{Name: valid.Name, Version: valid.Version, LibrarySHA256: strings.Repeat("0", 64)}, want: ErrDriverIdentityMismatch},
		{name: "missing digest", id: DriverIdentity{Name: valid.Name, Version: valid.Version}, want: ErrInvalidDriverRequest},
		{name: "noncanonical name", id: DriverIdentity{Name: "FlightSQL", Version: valid.Version, LibrarySHA256: valid.LibrarySHA256}, want: ErrInvalidDriverRequest},
		{name: "tag-breaking version", id: DriverIdentity{Name: valid.Name, Version: "1:0", LibrarySHA256: valid.LibrarySHA256}, want: ErrInvalidDriverRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mgr.RequiredCapabilities(tt.id)
			if !errors.Is(err, tt.want) {
				t.Fatalf("RequiredCapabilities(%+v) error = %v, want %v", tt.id, err, tt.want)
			}
			var diagnostic *CapabilityError
			if !errors.As(err, &diagnostic) {
				t.Fatalf("error %T does not expose CapabilityError", err)
			}
			if diagnostic.Identity != tt.id {
				t.Fatalf("diagnostic identity = %+v, want %+v", diagnostic.Identity, tt.id)
			}
		})
	}
}
