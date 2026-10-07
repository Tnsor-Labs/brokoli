package api

import (
	"errors"
	"net/http"
	"runtime"
	"sort"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

// DriverHandler serves the curated native ADBC driver catalog and this
// host's installed builds. Install and remove are host-wide operations:
// every worker on this host, for every workspace, loads what is installed.
type DriverHandler struct {
	store store.Store
	// manager overrides the process-wide inventory (tests).
	manager *drivers.Manager
}

func NewDriverHandler(s store.Store) *DriverHandler { return &DriverHandler{store: s} }

func (h *DriverHandler) drivers() (*drivers.Manager, error) {
	if h.manager != nil {
		return h.manager, nil
	}
	return drivers.Shared(drivers.DefaultDir())
}

type catalogDriver struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"display_name,omitempty"`
	Description string             `json:"description,omitempty"`
	Icon        string             `json:"icon,omitempty"`
	IconURL     string             `json:"icon_url,omitempty"`
	License     string             `json:"license,omitempty"`
	Homepage    string             `json:"homepage,omitempty"`
	DocsURL     string             `json:"docs_url,omitempty"`
	Lifecycle   string             `json:"lifecycle,omitempty"`
	ADBCVersion string             `json:"adbc_version,omitempty"`
	MinBrokoli  string             `json:"min_brokoli,omitempty"`
	Advisories  []drivers.Advisory `json:"advisories,omitempty"`
	Version     string             `json:"version"`
	OS          string             `json:"os"`
	Arch        string             `json:"arch"`
	SHA256      string             `json:"sha256"`
	Installed   bool               `json:"installed"`
	Available   bool               `json:"available"`
	// UsableBy lists the connection types that can read through this
	// driver; empty when none can yet.
	UsableBy []string `json:"usable_by"`
}

// installedDriver is one installed build: what a connection pins.
type installedDriver struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	Entrypoint    string   `json:"entrypoint"`
	LibrarySHA256 string   `json:"library_sha256"`
	UsableBy      []string `json:"usable_by"`
}

// usableBy is drivers.UsableBy as strings, never nil, for the API.
func usableBy(name string) []string {
	out := []string{}
	for _, k := range drivers.UsableBy(name) {
		out = append(out, string(k))
	}
	return out
}

func toInstalledDriver(manifest *drivers.Manifest) installedDriver {
	return installedDriver{Name: manifest.Name, Version: manifest.Version, OS: manifest.OS, Arch: manifest.Arch,
		Entrypoint: manifest.Entrypoint, LibrarySHA256: manifest.Identity().LibrarySHA256, UsableBy: usableBy(manifest.Name)}
}

func installedDrivers(manager *drivers.Manager) []installedDriver {
	out := []installedDriver{}
	for _, manifest := range manager.List() {
		out = append(out, toInstalledDriver(manifest))
	}
	return out
}

// Installed lists the builds installed on this host, for pinning a
// connection. It is behind authentication, unlike the capabilities
// document: exact versions of the native code a server loads are not
// something to tell anyone who asks.
func (h *DriverHandler) Installed(w http.ResponseWriter, r *http.Request) {
	manager, err := h.drivers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load installed drivers: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"native_worker_enabled": engine.NativeADBCWorkerEnabled(),
		"drivers":               installedDrivers(manager),
	})
}

// Catalog exposes the operator-selected curated catalog for this machine's
// platform. It never accepts a caller-provided package URL.
func (h *DriverHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	manager, err := h.drivers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load installed drivers: "+err.Error())
		return
	}
	platform := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}

	index, err := drivers.CachedIndex(r.Context())
	if errors.Is(err, drivers.ErrCatalogNotConfigured) {
		entries := make([]catalogDriver, 0)
		for _, manifest := range manager.List() {
			entries = append(entries, installedEntry(manifest))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"configured": false, "native_worker_enabled": engine.NativeADBCWorkerEnabled(), "platform": platform, "drivers": entries})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	entries := make([]catalogDriver, 0, len(index.Drivers))
	listed := make(map[string]bool)
	for _, entry := range index.Drivers {
		if entry.OS != runtime.GOOS || entry.Arch != runtime.GOARCH {
			continue
		}
		installed := false
		for _, manifest := range manager.List() {
			// The catalog's SHA-256 authenticates its archive while a saved
			// connection pins the installed library SHA-256. Catalog v1 does
			// not publish the latter, so the release name and version are the
			// strongest identity it can compare without re-downloading it.
			if manifest.Name == entry.Name && manifest.Version == entry.Version {
				installed = true
				break
			}
		}
		entries = append(entries, catalogDriver{
			Name: entry.Name, DisplayName: entry.DisplayName, Description: entry.Description, Icon: entry.Icon, IconURL: entry.IconURL, License: entry.License, Homepage: entry.Homepage,
			DocsURL: entry.DocsURL, Lifecycle: entry.Lifecycle, ADBCVersion: entry.ADBCVersion, MinBrokoli: entry.MinBrokoli, Advisories: entry.Advisories,
			Version: entry.Version, OS: entry.OS, Arch: entry.Arch, SHA256: entry.SHA256,
			Installed: installed, Available: entry.Installable(), UsableBy: usableBy(entry.Name),
		})
		listed[entry.Name+"\x00"+entry.Version] = true
	}
	// An installed build the catalog does not publish is still reported: it is
	// what connections on this host may be pinned to, and hiding it would make
	// those connections inexplicable.
	for _, manifest := range manager.List() {
		if !listed[manifest.Name+"\x00"+manifest.Version] {
			entries = append(entries, installedEntry(manifest))
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return drivers.CompareVersions(entries[i].Version, entries[j].Version) > 0
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"configured":            true,
		"native_worker_enabled": engine.NativeADBCWorkerEnabled(),
		"version":               index.Version,
		"platform":              platform,
		"drivers":               entries,
	})
}

func installedEntry(manifest *drivers.Manifest) catalogDriver {
	return catalogDriver{Name: manifest.Name, Version: manifest.Version, OS: manifest.OS, Arch: manifest.Arch, SHA256: manifest.ArchiveSHA256, Installed: true, UsableBy: usableBy(manifest.Name)}
}

// driverErrorStatus maps a driver operation's failure to an HTTP status: an
// upstream catalog problem is a bad gateway, not a bad request.
func driverErrorStatus(err error) int {
	switch {
	case errors.Is(err, drivers.ErrCatalogNotConfigured):
		return http.StatusConflict
	case errors.Is(err, drivers.ErrNotInCatalog):
		return http.StatusNotFound
	case errors.Is(err, drivers.ErrCatalogUnavailable), errors.Is(err, drivers.ErrDigestMismatch):
		return http.StatusBadGateway
	default:
		return http.StatusUnprocessableEntity
	}
}

// Install installs a named, platform-compatible catalog release. It accepts no
// archive URL or digest from the caller.
func (h *DriverHandler) Install(w http.ResponseWriter, r *http.Request) {
	manager, err := h.drivers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load installed drivers: "+err.Error())
		return
	}
	installed, err := manager.InstallFromCatalogVersion(r.Context(), chi.URLParam(r, "name"), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, driverErrorStatus(err), "install driver: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toInstalledDriver(installed))
}

// Documentation returns catalog-approved Markdown for one platform release.
func (h *DriverHandler) Documentation(w http.ResponseWriter, r *http.Request) {
	documentation, err := drivers.Documentation(r.Context(), chi.URLParam(r, "name"), chi.URLParam(r, "version"))
	if err != nil {
		writeError(w, driverErrorStatus(err), "driver documentation: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"markdown": documentation})
}

// Remove deletes an installed driver release, refusing while a saved
// connection is pinned to it. Removing a pinned release would leave those
// pipelines failing at run time with a driver error instead of a clear
// statement made now, at the moment of the destructive action.
func (h *DriverHandler) Remove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	version := r.URL.Query().Get("version")
	manager, err := h.drivers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load installed drivers: "+err.Error())
		return
	}
	targets := installedTargets(manager, name, version)
	if len(targets) == 0 {
		writeError(w, http.StatusNotFound, "driver is not installed")
		return
	}
	pinned, elsewhere, err := h.pinnedConnections(targets, GetWorkspaceID(r))
	if err != nil {
		// The pinned check is the whole point; a store that cannot answer
		// it must not let the removal through.
		writeError(w, http.StatusServiceUnavailable, "cannot check which connections use this driver: "+err.Error())
		return
	}
	if len(pinned) > 0 || elsewhere > 0 {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "connections are pinned to this driver release",
			// Only connections in the caller's own workspace are named; the
			// rest are counted. The driver is host-wide, its users are not.
			"conns":                 pinned,
			"other_workspace_count": elsewhere,
			"detail":                "Repoint or delete these connections before removing the driver release.",
		})
		return
	}
	if version == "" {
		err = manager.Remove(name)
	} else {
		err = manager.RemoveVersion(name, version)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "remove driver: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func installedTargets(manager *drivers.Manager, name, version string) []models.DriverIdentity {
	var targets []models.DriverIdentity
	for _, manifest := range manager.List() {
		if manifest.Name != name || version != "" && manifest.Version != version {
			continue
		}
		targets = append(targets, manifest.Identity())
	}
	return targets
}

// pinnedConnections returns the IDs of connections in workspaceID pinned to
// any target, and how many such connections other workspaces have.
func (h *DriverHandler) pinnedConnections(targets []models.DriverIdentity, workspaceID string) ([]string, int, error) {
	if h.store == nil {
		return nil, 0, nil
	}
	wanted := make(map[string]bool, len(targets))
	for _, target := range targets {
		wanted[target.Key()] = true
	}
	// ListConnections spans every workspace but does not carry each
	// connection's workspace, so the caller's own come from a scoped list
	// and the rest are the difference.
	all, err := h.store.ListConnections()
	if err != nil {
		return nil, 0, err
	}
	mine, err := h.store.ListConnectionsByWorkspace(workspaceID)
	if err != nil {
		return nil, 0, err
	}
	isPinned := func(c models.Connection) bool {
		return c.DriverIdentity != nil && wanted[c.DriverIdentity.Key()]
	}
	pinned := []string{}
	for _, connection := range mine {
		if isPinned(connection) {
			pinned = append(pinned, connection.ConnID)
		}
	}
	total := 0
	for _, connection := range all {
		if isPinned(connection) {
			total++
		}
	}
	elsewhere := total - len(pinned)
	if elsewhere < 0 {
		elsewhere = 0
	}
	sort.Strings(pinned)
	return pinned, elsewhere, nil
}
