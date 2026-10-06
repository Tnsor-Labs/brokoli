package api

import (
	"net/http"
	"runtime"
	"sort"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

// DriverHandler serves the curated native ADBC driver catalog and the local
// installation inventory. Install and remove are host-wide operations: every
// worker on this server executes the same installed artifact.
type DriverHandler struct {
	store store.Store
}

func NewDriverHandler(s store.Store) *DriverHandler { return &DriverHandler{store: s} }

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
}

func (h *DriverHandler) manager() (*drivers.Manager, error) {
	return drivers.NewManager(drivers.DefaultDir())
}

// Catalog exposes the operator-selected curated catalog for this machine's
// platform. It never accepts a caller-provided package URL.
func (h *DriverHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	// The managed catalog can change independently of the application build;
	// never let a browser or development proxy retain an older release list.
	w.Header().Set("Cache-Control", "no-store")
	manager, err := h.manager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load installed drivers: "+err.Error())
		return
	}

	indexURL := drivers.IndexURL()
	if indexURL == "" {
		entries := make([]catalogDriver, 0, len(manager.List()))
		for _, manifest := range manager.List() {
			entries = append(entries, catalogDriver{Name: manifest.Name, Version: manifest.Version, OS: manifest.OS, Arch: manifest.Arch, SHA256: manifest.ArchiveSHA256, Installed: true})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"configured": false, "platform": map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}, "drivers": entries})
		return
	}
	index, err := drivers.FetchIndex(r.Context(), indexURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "fetch driver catalog: "+err.Error())
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
			Installed: installed, Available: true,
		})
		listed[entry.Name] = true
	}
	// An installed build the catalog does not publish is still reported: it is
	// what connections on this host may be pinned to, and hiding it would make
	// those connections inexplicable.
	for _, manifest := range manager.List() {
		if listed[manifest.Name] {
			continue
		}
		entries = append(entries, catalogDriver{Name: manifest.Name, Version: manifest.Version, OS: manifest.OS, Arch: manifest.Arch, SHA256: manifest.ArchiveSHA256, Installed: true})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Version > entries[j].Version
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"configured": true,
		"version":    index.Version,
		"platform":   map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
		"drivers":    entries,
	})
}

// Install installs a named, platform-compatible catalog release. It accepts no
// archive URL or digest from the caller.
func (h *DriverHandler) Install(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	manager, err := h.manager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load installed drivers: "+err.Error())
		return
	}
	installed, err := manager.InstallFromCatalogVersion(r.Context(), name, r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "install curated driver: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"name": installed.Name, "version": installed.Version, "os": installed.OS, "arch": installed.Arch,
		"entrypoint": installed.Entrypoint, "library_sha256": installed.LibrarySHA256,
	})
}

// Documentation returns catalog-approved Markdown for one platform release.
func (h *DriverHandler) Documentation(w http.ResponseWriter, r *http.Request) {
	documentation, err := drivers.Documentation(r.Context(), chi.URLParam(r, "name"), chi.URLParam(r, "version"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "fetch driver documentation: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"markdown": documentation})
}

// Remove deletes an installed driver release, refusing while a saved
// connection is pinned to that exact identity. Removing a pinned release
// would leave those pipelines failing at run time with a driver error instead
// of a clear statement made now, at the moment of the destructive action.
func (h *DriverHandler) Remove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	version := r.URL.Query().Get("version")
	manager, err := h.manager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load installed drivers: "+err.Error())
		return
	}
	if version == "" && manager.Get(name) == nil {
		writeError(w, http.StatusNotFound, "driver is not installed")
		return
	}

	targets := h.installedTargets(manager, name, version)
	if len(targets) == 0 {
		writeError(w, http.StatusNotFound, "driver is not installed")
		return
	}
	if pinned := h.pinnedConnections(targets); len(pinned) > 0 {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":  "connections are pinned to this driver release",
			"conns":  pinned,
			"detail": "Repoint or delete these connections before removing the driver release.",
		})
		return
	}

	var removeErr error
	if version == "" {
		removeErr = manager.Remove(name)
	} else {
		removeErr = manager.RemoveVersion(name, version)
	}
	if err := removeErr; err != nil {
		writeError(w, http.StatusInternalServerError, "remove driver: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DriverHandler) installedTargets(manager *drivers.Manager, name, version string) []drivers.DriverIdentity {
	var targets []drivers.DriverIdentity
	for _, manifest := range manager.List() {
		if manifest.Name != name || version != "" && manifest.Version != version {
			continue
		}
		targets = append(targets, drivers.DriverIdentity{Name: manifest.Name, Version: manifest.Version, LibrarySHA256: manifest.LibrarySHA256})
	}
	return targets
}

// pinnedConnections lists saved connections that would stop resolving.
func (h *DriverHandler) pinnedConnections(targets []drivers.DriverIdentity) []string {
	if h.store == nil {
		return nil
	}
	wanted := make(map[string]bool, len(targets))
	for _, target := range targets {
		wanted[driverIdentityKey(target)] = true
	}
	connections, err := h.store.ListConnections()
	if err != nil {
		// A store that cannot list connections must not silently permit the
		// removal; refuse instead, since the pinned check is the whole point.
		return []string{"(connection inventory unavailable)"}
	}
	var pinned []string
	for _, connection := range connections {
		if connection.DriverIdentity == nil {
			continue
		}
		if wanted[driverIdentityKey(*connection.DriverIdentity)] {
			pinned = append(pinned, connection.ConnID)
		}
	}
	sort.Strings(pinned)
	return pinned
}

func driverIdentityKey(identity drivers.DriverIdentity) string {
	return identity.Name + "\x00" + identity.Version + "\x00" + identity.LibrarySHA256
}

var _ = models.ConnTypeFlightSQL
