package api

// Task bundle v2 API (ADR-033): the host's read/write endpoints for
// tenant-scoped, content-addressed task-bundle/v2 archives -- the byte
// store a 'task' IR node's bundle reference resolves against at run
// time. Distinct from and additive alongside the task-bundle/1 endpoints
// in handlers_taskbundles.go (ADR-035 Decision 1); see that file's doc
// comment for the shared upload/fetch contract this mirrors exactly
// (URL-named digest, re-hash-and-verify before persisting, byte-identical
// re-upload is a no-op, a different archive at an occupied digest is a
// 409).
//
// Upload validates the manifest by fully extracting the archive into a
// scratch directory with pkg/taskbundlev2.Extract (discarded immediately
// after) rather than a lighter parse-only pass -- task-bundle/v2's
// manifest carries a size+sha256 per declared file (section 2 rule 3),
// so this also catches a content/digest mismatch at upload time, not
// merely a structural manifest problem.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

// TaskBundleV2Handler serves the task-bundle/v2 endpoints.
type TaskBundleV2Handler struct {
	store store.Store
}

func NewTaskBundleV2Handler(s store.Store) *TaskBundleV2Handler {
	return &TaskBundleV2Handler{store: s}
}

func (h *TaskBundleV2Handler) bundleStore(w http.ResponseWriter) store.TaskBundleV2Store {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "task bundle storage is not available on this server")
		return nil
	}
	sb, ok := h.store.(store.TaskBundleV2Store)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "task bundle storage is not available on this server")
		return nil
	}
	return sb
}

func (h *TaskBundleV2Handler) uploadOrg(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := GetOrgIDFromRequest(r)
	if orgID != "" {
		return orgID, true
	}
	if OrgResolverFunc != nil {
		writeError(w, http.StatusBadRequest,
			"cannot upload task bundle: user has no organization membership")
		return "", false
	}
	return "", true
}

// Upload accepts a task-bundle/v2 archive as the raw request body and
// stores it content-addressed under the URL's digest.
func (h *TaskBundleV2Handler) Upload(w http.ResponseWriter, r *http.Request) {
	sb := h.bundleStore(w)
	if sb == nil {
		return
	}
	orgID, ok := h.uploadOrg(w, r)
	if !ok {
		return
	}
	digest := chi.URLParam(r, "digest")
	if !taskbundlev2.IsDigest(digest) {
		writeError(w, http.StatusBadRequest, "digest must be a content address of the form \"sha256:<64 hex chars>\"")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, taskbundlev2.MaxArchiveBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read uploaded archive")
		return
	}
	if len(body) > taskbundlev2.MaxArchiveBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("task bundle exceeds the %d-byte limit", int64(taskbundlev2.MaxArchiveBytes)))
		return
	}

	actual := taskbundlev2.DigestOf(body)
	if actual != digest {
		writeError(w, http.StatusBadRequest,
			"archive hashes to "+actual+" but was claimed as "+digest)
		return
	}

	scratch, err := os.MkdirTemp("", "brokoli-taskbundlev2-upload-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(scratch)
	if _, err := taskbundlev2.Extract(body, scratch); err != nil {
		writeError(w, http.StatusBadRequest, "task bundle is invalid: "+err.Error())
		return
	}

	created, err := sb.PutTaskBundleV2(orgID, digest, body)
	if err != nil {
		if errors.Is(err, store.ErrTaskBundleV2Collision) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := http.StatusOK
	state := "unchanged"
	if created {
		status = http.StatusCreated
		state = "stored"
	}
	writeJSON(w, status, map[string]interface{}{
		"status":     state,
		"digest":     digest,
		"format":     taskbundlev2.Format,
		"size_bytes": len(body),
	})
}

// Get serves the stored archive bytes for an org-scoped digest.
func (h *TaskBundleV2Handler) Get(w http.ResponseWriter, r *http.Request) {
	sb := h.bundleStore(w)
	if sb == nil {
		return
	}
	orgID := GetOrgIDFromRequest(r)
	digest := chi.URLParam(r, "digest")
	if !taskbundlev2.IsDigest(digest) {
		writeError(w, http.StatusBadRequest, "digest must be a content address of the form \"sha256:<64 hex chars>\"")
		return
	}
	archive, err := sb.GetTaskBundleV2(orgID, digest)
	if err != nil {
		if errors.Is(err, store.ErrTaskBundleV2NotFound) {
			writeError(w, http.StatusNotFound, "no task bundle with digest "+digest+" for this org")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-SHA256", digest)
	w.Header().Set("ETag", `"`+digest+`"`)
	if match := r.Header.Get("If-None-Match"); match == `"`+digest+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.Copy(w, bytes.NewReader(archive))
}
