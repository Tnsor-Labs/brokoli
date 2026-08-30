package store

import (
	"bytes"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundle"
)

// A task bundle store is content-addressed and org-scoped: identity IS
// (org_id, digest), so identical re-uploads are no-ops, different bytes
// under a taken digest are collisions, and one tenant can never read or
// overwrite another's archive even under the same digest reference.

func testBundles(t *testing.T) []byte {
	t.Helper()
	archive, err := taskbundle.Assemble(
		map[string]string{"tasks.py": "output_data = {'columns': [], 'rows': []}\n"},
		&taskbundle.Manifest{Format: taskbundle.Format, Language: "python", Entry: "tasks.py", Files: []string{"tasks.py"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func seedTaskBundles(t *testing.T) (Store, TaskBundleStore) {
	t.Helper()
	s := newTestStore(t) // SQLite-backed TestStore
	archive := testBundles(t)
	digest := taskbundle.DigestOf(archive)
	created, err := s.PutTaskBundle("org-a", digest, archive)
	if err != nil {
		t.Fatalf("PutTaskBundle: %v", err)
	}
	if !created {
		t.Fatal("first upload must report created=true")
	}
	return s, s
}

func TestTaskBundleStore_IdenticalReuploadIsNoop(t *testing.T) {
	_, sb := seedTaskBundles(t)
	archive := testBundles(t)
	digest := taskbundle.DigestOf(archive)
	created, err := sb.PutTaskBundle("org-a", digest, archive)
	if err != nil {
		t.Fatalf("re-upload: %v", err)
	}
	if created {
		t.Fatal("byte-identical re-upload must report created=false")
	}
	got, err := sb.GetTaskBundle("org-a", digest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, archive) {
		t.Fatal("stored bytes differ from the archive")
	}
}

func TestTaskBundleStore_CollisionWithDifferentBytes(t *testing.T) {
	_, sb := seedTaskBundles(t)
	archive := testBundles(t)
	// Different bytes, same digest claim is impossible to build with a
	// real sha256 — so drive the collision at the store method: a caller
	// that passes a digest its bytes do NOT hash to must be refused, and
	// a second DIFFERENT archive under a fresh digest is a separate key.
	other, err := taskbundle.Assemble(
		map[string]string{"tasks.py": "output_data = {'columns': [], 'rows': []}\n# v2\n"},
		&taskbundle.Manifest{Format: taskbundle.Format, Language: "python", Entry: "tasks.py", Files: []string{"tasks.py"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes under the *wrong* digest: refused outright (caller bug,
	// not stored under a lying key).
	wrong := "sha256:" + string(bytes.Repeat([]byte("0"), 64))
	if _, err := sb.PutTaskBundle("org-a", wrong, archive); err == nil {
		t.Fatal("an upload whose bytes do not hash to the claimed digest was accepted")
	}
	// A genuinely different archive under its own digest is fine.
	otherDigest := taskbundle.DigestOf(other)
	if created, err := sb.PutTaskBundle("org-a", otherDigest, other); err != nil || !created {
		t.Fatalf("second distinct archive: created=%v err=%v", created, err)
	}
}

func TestTaskBundleStore_TenantIsolation(t *testing.T) {
	_, sb := seedTaskBundles(t)
	archive := testBundles(t)
	digest := taskbundle.DigestOf(archive)
	if _, err := sb.GetTaskBundle("org-b", digest); err != ErrTaskBundleNotFound {
		t.Fatalf("org-b must not read org-a's bundle: %v", err)
	}
	// org-b may store its own bytes under the SAME digest reference (its
	// own provenance), and org-a's copy must be untouched.
	theirArchive := testBundles(t)
	if created, err := sb.PutTaskBundle("org-b", digest, theirArchive); err != nil || !created {
		t.Fatalf("org-b first upload: created=%v err=%v", created, err)
	}
	gotA, _ := sb.GetTaskBundle("org-a", digest)
	if !bytes.Equal(gotA, archive) {
		t.Fatal("org-a's bundle was clobbered by org-b's identical-digest upload")
	}
	gotB, _ := sb.GetTaskBundle("org-b", digest)
	if !bytes.Equal(gotB, theirArchive) {
		t.Fatal("org-b's bundle does not match what it uploaded")
	}
}

func TestTaskBundleStore_GetMissingIsNotFound(t *testing.T) {
	_, sb := seedTaskBundles(t)
	if _, err := sb.GetTaskBundle("org-a", "sha256:"+string(bytes.Repeat([]byte("a"), 64))); err != ErrTaskBundleNotFound {
		t.Fatalf("missing bundle: got %v, want ErrTaskBundleNotFound", err)
	}
}

func TestTaskBundleStore_StaticCapabilityInterface(t *testing.T) {
	// The capability must be reachable by type assertion from the
	// concrete store a caller actually holds, exactly as the engine and
	// API handlers reach it.
	s := newTestStore(t)
	var storeIntf Store = s
	if _, ok := storeIntf.(TaskBundleStore); !ok {
		t.Fatal("SQLite-backed TestStore is not reachable as TaskBundleStore; engine/API will 503 on task bundles")
	}
}