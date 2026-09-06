package store

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
)

// TaskBundleV2Store mirrors TaskBundleStore's contract exactly (ADR-033
// vs ADR-031, distinct namespaces) -- see store/taskbundle_test.go's own
// doc comment for the invariants under test here.

// buildV2Archive builds a minimal, valid task-bundle/v2 archive around
// one Python module -- just enough to exercise the store's content-
// addressing contract; execution semantics aren't this test's concern
// (see engine/task_exec_test.go for that).
func buildV2Archive(t *testing.T, source string) []byte {
	t.Helper()
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	archive, err := taskbundlev2.Assemble(
		map[string]string{"fixture_task.py": source},
		&taskbundlev2.Manifest{
			Format:          taskbundlev2.Format,
			Name:            "fixture-task",
			InterfaceDigest: digest,
			SourceDigest:    digest,
			Payloads: []taskbundlev2.Payload{{
				ID:            "python-any",
				Runtime:       taskbundlev2.RuntimePython,
				OS:            "any",
				Arch:          "any",
				Entrypoint:    taskbundlev2.Entrypoint{Module: "fixture_task", Symbol: "run"},
				Effects:       taskbundlev2.EffectPure,
				PayloadDigest: digest,
			}},
		},
	)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return archive
}

func testBundlesV2(t *testing.T) []byte {
	t.Helper()
	return buildV2Archive(t, "def run():\n    return 1\n")
}

func seedTaskBundlesV2(t *testing.T) (Store, TaskBundleV2Store) {
	t.Helper()
	s := newTestStore(t)
	archive := testBundlesV2(t)
	digest := taskbundlev2.DigestOf(archive)
	created, err := s.PutTaskBundleV2("org-a", digest, archive)
	if err != nil {
		t.Fatalf("PutTaskBundleV2: %v", err)
	}
	if !created {
		t.Fatal("first upload must report created=true")
	}
	return s, s
}

func TestTaskBundleV2Store_IdenticalReuploadIsNoop(t *testing.T) {
	_, sb := seedTaskBundlesV2(t)
	archive := testBundlesV2(t)
	digest := taskbundlev2.DigestOf(archive)
	created, err := sb.PutTaskBundleV2("org-a", digest, archive)
	if err != nil {
		t.Fatalf("re-upload: %v", err)
	}
	if created {
		t.Fatal("byte-identical re-upload must report created=false")
	}
	got, err := sb.GetTaskBundleV2("org-a", digest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, archive) {
		t.Fatal("stored bytes differ from the archive")
	}
}

func TestTaskBundleV2Store_CollisionWithDifferentBytes(t *testing.T) {
	_, sb := seedTaskBundlesV2(t)
	archive := testBundlesV2(t)
	other := buildV2Archive(t, "def run():\n    return 2\n")

	wrong := "sha256:" + strings.Repeat("0", 64)
	if _, err := sb.PutTaskBundleV2("org-a", wrong, archive); err == nil {
		t.Fatal("an upload whose bytes do not hash to the claimed digest was accepted")
	}
	otherDigest := taskbundlev2.DigestOf(other)
	if created, err := sb.PutTaskBundleV2("org-a", otherDigest, other); err != nil || !created {
		t.Fatalf("second distinct archive: created=%v err=%v", created, err)
	}
}

func TestTaskBundleV2Store_TenantIsolation(t *testing.T) {
	_, sb := seedTaskBundlesV2(t)
	archive := testBundlesV2(t)
	digest := taskbundlev2.DigestOf(archive)
	if _, err := sb.GetTaskBundleV2("org-b", digest); err != ErrTaskBundleV2NotFound {
		t.Fatalf("org-b must not read org-a's bundle: %v", err)
	}
	theirArchive := testBundlesV2(t)
	if created, err := sb.PutTaskBundleV2("org-b", digest, theirArchive); err != nil || !created {
		t.Fatalf("org-b first upload: created=%v err=%v", created, err)
	}
	gotA, _ := sb.GetTaskBundleV2("org-a", digest)
	if !bytes.Equal(gotA, archive) {
		t.Fatal("org-a's bundle was clobbered by org-b's identical-digest upload")
	}
	gotB, _ := sb.GetTaskBundleV2("org-b", digest)
	if !bytes.Equal(gotB, theirArchive) {
		t.Fatal("org-b's bundle does not match what it uploaded")
	}
}

func TestTaskBundleV2Store_GetMissingIsNotFound(t *testing.T) {
	_, sb := seedTaskBundlesV2(t)
	if _, err := sb.GetTaskBundleV2("org-a", "sha256:"+strings.Repeat("a", 64)); err != ErrTaskBundleV2NotFound {
		t.Fatalf("missing bundle: got %v, want ErrTaskBundleV2NotFound", err)
	}
}

func TestTaskBundleV2Store_StaticCapabilityInterface(t *testing.T) {
	s := newTestStore(t)
	var storeIntf Store = s
	if _, ok := storeIntf.(TaskBundleV2Store); !ok {
		t.Fatal("SQLite-backed TestStore is not reachable as TaskBundleV2Store; engine/API will 503 on task-bundle/v2")
	}
}
