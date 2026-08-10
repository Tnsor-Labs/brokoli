package api

// Plugin management API (#110 M2, ADR-016): list installed plugins,
// install from an uploaded .bkg archive, remove, and serve a plugin's
// source archive for worker fetch-by-digest. Installs and removals
// hot-reload the shared plugin manager, so node types become
// resolvable (or disappear) without a server restart.
//
// Guarded by PermSettingsEdit — installing a plugin runs its code on the
// host, so it is an admin-only operation, same trust level as editing
// server settings.

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
	"github.com/go-chi/chi/v5"
)

// maxPluginUploadBytes caps an uploaded archive before the package
// layer's own per-file/total extraction limits apply.
const maxPluginUploadBytes = 128 << 20 // 128 MiB

// PluginHandler serves the plugin management endpoints against a shared
// *plugins.Manager. Nil manager means the host has no plugin support
// wired (never expected in serve.go, but the handlers degrade to 503
// rather than panicking).
type PluginHandler struct {
	mgr *plugins.Manager
}

func NewPluginHandler(mgr *plugins.Manager) *PluginHandler {
	return &PluginHandler{mgr: mgr}
}

type pluginNodeTypeDTO struct {
	Type        string `json:"type"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name,omitempty"`
}

type pluginDTO struct {
	Name       string              `json:"name"`
	Version    string              `json:"version"`
	Descr      string              `json:"description,omitempty"`
	NodeTypes  []pluginNodeTypeDTO `json:"node_types"`
	Packaged   bool                `json:"packaged"` // installed from a .bkg (has a re-servable archive)
	ArchiveSHA string              `json:"archive_sha256,omitempty"`
}

func toPluginDTO(m *plugins.Manifest) pluginDTO {
	nts := make([]pluginNodeTypeDTO, 0, len(m.NodeTypes))
	for _, nt := range m.NodeTypes {
		nts = append(nts, pluginNodeTypeDTO{Type: nt.Type, Kind: string(nt.Kind), DisplayName: nt.DisplayName})
	}
	dto := pluginDTO{Name: m.Name, Version: m.Version, Descr: m.Description, NodeTypes: nts}
	if _, sha, ok := plugins.SourceArchivePath(m.Dir()); ok {
		dto.Packaged = true
		dto.ArchiveSHA = sha
	}
	return dto
}

func (h *PluginHandler) unavailable(w http.ResponseWriter) bool {
	if h.mgr == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin support is not enabled on this server")
		return true
	}
	return false
}

// List returns installed plugins.
func (h *PluginHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	installed := h.mgr.List()
	out := make([]pluginDTO, 0, len(installed))
	for _, m := range installed {
		out = append(out, toPluginDTO(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// Install accepts a .bkg archive as the raw request body (or a
// multipart "file" field) and installs it, then hot-reloads the manager.
func (h *PluginHandler) Install(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	tmp, err := os.CreateTemp("", "brokoli-upload-*.bkg")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	body := archiveReader(r)
	if _, err := io.Copy(tmp, io.LimitReader(body, maxPluginUploadBytes+1)); err != nil {
		tmp.Close()
		writeError(w, http.StatusBadRequest, "failed to read uploaded archive")
		return
	}
	tmp.Close()
	if info, _ := os.Stat(tmpPath); info != nil && info.Size() > maxPluginUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("archive exceeds the %d-byte upload limit", int64(maxPluginUploadBytes)))
		return
	}

	installed, err := plugins.InstallArchive(tmpPath, h.mgr.Dir())
	if err != nil {
		// Feasibility/integrity/drift failures are the client's problem
		// to fix (wrong platform, tampered file); surface as 400.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := plugins.StashSourceArchive(installed.Dir(), tmpPath); err != nil {
		// Non-fatal: the plugin is installed and usable; only the
		// re-serve-for-workers path is affected. Log-shaped response.
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"plugin":  toPluginDTO(installed),
			"warning": "installed, but the source archive could not be stashed for worker fetch: " + err.Error(),
		})
		return
	}
	// Hot reload so the freshly installed node types resolve without a
	// restart. LoadAll is concurrency-safe.
	if err := h.mgr.LoadAll(); err != nil {
		writeError(w, http.StatusInternalServerError, "installed but reload failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toPluginDTO(installed))
}

// Remove uninstalls a plugin and hot-reloads.
func (h *PluginHandler) Remove(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	name := chi.URLParam(r, "name")
	if h.mgr.Get(name) == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("plugin %q is not installed", name))
		return
	}
	if err := h.mgr.Remove(name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Archive serves a plugin's stashed source archive, so a worker can
// fetch it by digest and run its own runtime resolution (#110 M2).
// The digest is advertised in the ETag and an explicit header.
func (h *PluginHandler) Archive(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	name := chi.URLParam(r, "name")
	m := h.mgr.Get(name)
	if m == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("plugin %q is not installed", name))
		return
	}
	path, sha, ok := plugins.SourceArchivePath(m.Dir())
	if !ok {
		writeError(w, http.StatusConflict, fmt.Sprintf("plugin %q was installed from a directory, not a package archive, and has no archive to serve", name))
		return
	}
	f, err := os.Open(path) // #nosec G304 -- path from the manager's own plugin dir
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-SHA256", sha)
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".bkg"))
	if match := r.Header.Get("If-None-Match"); match == `"`+sha+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.Copy(w, f)
}

// archiveReader returns the archive bytes from either a multipart "file"
// field or the raw request body.
func archiveReader(r *http.Request) io.Reader {
	if file, _, err := r.FormFile("file"); err == nil {
		return file
	}
	return r.Body
}
