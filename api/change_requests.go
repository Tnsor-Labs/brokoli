package api

// Change requests (ADR-046 phase 3).
//
// A change request is a proposed new version of a pipeline that does not
// run until a person applies it. An agent changes a pipeline only this
// way; a person who may not edit a pipeline can propose to one too. The
// request keeps the whole proposed definition, the version it was made
// against, and its validation; the diff against the pipeline is computed
// when it is read.
//
// It is applied by someone with pipelines.edit who is a person, through
// the same save an edit uses (saveUpdate). When the pipeline has moved on
// since the request was made, the request is stale and cannot be applied:
// its definition would overwrite changes its author never saw.
//
// A pipeline whose definition lives elsewhere (deployed from a repository)
// is not changed by applying a request in place. A distribution that can
// carry the change there sets ChangeRequestDeliverFunc; without one, such
// a pipeline takes no change requests.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// Change request states. Stale is not stored: it is an open request whose
// base version is no longer the pipeline's.
const (
	ChangeOpen     = "open"
	ChangeApplied  = "applied"
	ChangeDeclined = "declined"
)

// ErrChangeNotFound is a change request that does not exist.
var ErrChangeNotFound = errors.New("change request not found")

// ChangeDelivery says where a distribution sent a change request.
type ChangeDelivery struct {
	Kind string `json:"kind"`          // e.g. "pull_request"
	URL  string `json:"url,omitempty"` // where a person reviews it
	Ref  string `json:"ref,omitempty"` // the distribution's own reference
}

// ChangeRequestDeliverFunc, when set, delivers a change request to a
// pipeline whose source is not Brokoli (one deployed from a repository).
// It is called after the request is recorded; what it returns is stored
// on the request. A distribution then records the outcome with
// SetChangeRequestOutcome when the change is merged or closed there.
var ChangeRequestDeliverFunc func(r *http.Request, cr *ChangeRequest, pipeline *models.Pipeline) (*ChangeDelivery, error)

// ChangeValidation is what validating the proposed definition said.
type ChangeValidation struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
}

// ChangeRequest is a proposed new version of a pipeline.
type ChangeRequest struct {
	ID          string           `json:"id"`
	PipelineID  string           `json:"pipeline_id"`
	Title       string           `json:"title"`
	Description string           `json:"description,omitempty"`
	Status      string           `json:"status"`
	Stale       bool             `json:"stale"`
	BaseVersion int              `json:"base_version"`
	Validation  ChangeValidation `json:"validation"`
	Proposed    *models.Pipeline `json:"proposed,omitempty"`
	Diff        *PipelineDiff    `json:"diff,omitempty"`
	Delivery    *ChangeDelivery  `json:"delivery,omitempty"`

	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedByKind string    `json:"created_by_kind"` // person or agent
	OnBehalfOf    string    `json:"on_behalf_of,omitempty"`
	CreatedAt     time.Time `json:"created_at"`

	DecidedBy      string     `json:"decided_by,omitempty"`
	DecidedByName  string     `json:"decided_by_name,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	AppliedVersion int        `json:"applied_version,omitempty"`
}

// ChangeRequestStore keeps change requests next to the pipelines.
type ChangeRequestStore struct {
	db      *sql.DB
	dialect string
}

// NewChangeRequestStore creates the store and its table.
func NewChangeRequestStore(db *sql.DB) (*ChangeRequestStore, error) {
	cs := &ChangeRequestStore{db: db, dialect: detectUserStoreDialect(db)}
	create := func() error {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS change_requests (
		id TEXT PRIMARY KEY,
		pipeline_id TEXT NOT NULL,
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		base_version INTEGER NOT NULL,
		proposed TEXT NOT NULL,
		validation TEXT NOT NULL DEFAULT '{}',
		delivery TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL,
		created_by_name TEXT NOT NULL DEFAULT '',
		created_by_kind TEXT NOT NULL DEFAULT 'person',
		on_behalf_of TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		decided_by TEXT NOT NULL DEFAULT '',
		decided_by_name TEXT NOT NULL DEFAULT '',
		decided_at TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		applied_version INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
			return fmt.Errorf("create change_requests: %w", err)
		}
		_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_change_requests_pipeline ON change_requests (pipeline_id, created_at)`)
		return err
	}
	// Every API process creates this at startup; on Postgres they take
	// turns under the store's migration lock rather than racing on the
	// catalog.
	var err error
	if cs.dialect == "postgres" {
		err = store.WithMigrationLock(db, create)
	} else {
		err = create()
	}
	return cs, err
}

func (cs *ChangeRequestStore) q(query string) string { return dialectQuery(cs.dialect, query) }

func (cs *ChangeRequestStore) create(cr *ChangeRequest) error {
	proposed, err := json.Marshal(cr.Proposed)
	if err != nil {
		return err
	}
	validation, _ := json.Marshal(cr.Validation)
	_, err = cs.db.Exec(cs.q(`INSERT INTO change_requests (id, pipeline_id, title, description, status, base_version, proposed,
		validation, created_by, created_by_name, created_by_kind, on_behalf_of, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		cr.ID, cr.PipelineID, cr.Title, cr.Description, cr.Status, cr.BaseVersion, string(proposed), string(validation),
		cr.CreatedBy, cr.CreatedByName, cr.CreatedByKind, cr.OnBehalfOf, cr.CreatedAt.Format(time.RFC3339Nano))
	return err
}

const changeColumns = `id, pipeline_id, title, description, status, base_version, proposed, validation, delivery,
	created_by, created_by_name, created_by_kind, on_behalf_of, created_at,
	decided_by, decided_by_name, decided_at, reason, applied_version`

func scanChange(row interface{ Scan(...any) error }) (*ChangeRequest, error) {
	var cr ChangeRequest
	var proposed, validation, delivery, created, decided string
	if err := row.Scan(&cr.ID, &cr.PipelineID, &cr.Title, &cr.Description, &cr.Status, &cr.BaseVersion, &proposed, &validation,
		&delivery, &cr.CreatedBy, &cr.CreatedByName, &cr.CreatedByKind, &cr.OnBehalfOf, &created,
		&cr.DecidedBy, &cr.DecidedByName, &decided, &cr.Reason, &cr.AppliedVersion); err != nil {
		return nil, err
	}
	cr.Proposed = &models.Pipeline{}
	if err := json.Unmarshal([]byte(proposed), cr.Proposed); err != nil {
		return nil, fmt.Errorf("read change request %s: %w", cr.ID, err)
	}
	_ = json.Unmarshal([]byte(validation), &cr.Validation)
	if delivery != "" {
		cr.Delivery = &ChangeDelivery{}
		_ = json.Unmarshal([]byte(delivery), cr.Delivery)
	}
	cr.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if decided != "" {
		if t, err := time.Parse(time.RFC3339Nano, decided); err == nil {
			cr.DecidedAt = &t
		}
	}
	return &cr, nil
}

func (cs *ChangeRequestStore) get(pipelineID, id string) (*ChangeRequest, error) {
	cr, err := scanChange(cs.db.QueryRow(cs.q(`SELECT `+changeColumns+` FROM change_requests WHERE id = ? AND pipeline_id = ?`), id, pipelineID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrChangeNotFound
	}
	return cr, err
}

func (cs *ChangeRequestStore) list(pipelineID, status string, limit int) ([]ChangeRequest, error) {
	query := `SELECT ` + changeColumns + ` FROM change_requests WHERE pipeline_id = ?`
	args := []any{pipelineID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := cs.db.Query(cs.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeRequest{}
	for rows.Next() {
		cr, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cr)
	}
	return out, rows.Err()
}

// listRecent is the newest requests across pipelines, with a status if
// given. The caller keeps those it may see.
func (cs *ChangeRequestStore) listRecent(status string, limit int) ([]ChangeRequest, error) {
	query := `SELECT ` + changeColumns + ` FROM change_requests`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := cs.db.Query(cs.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeRequest{}
	for rows.Next() {
		cr, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cr)
	}
	return out, rows.Err()
}

// decide settles an open request once. False when it was not open.
func (cs *ChangeRequestStore) decide(id, status, by, byName, reason string, appliedVersion int) (bool, error) {
	res, err := cs.db.Exec(cs.q(`UPDATE change_requests SET status = ?, decided_by = ?, decided_by_name = ?, decided_at = ?,
		reason = ?, applied_version = ? WHERE id = ? AND status = ?`),
		status, by, byName, time.Now().UTC().Format(time.RFC3339Nano), reason, appliedVersion, id, ChangeOpen)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (cs *ChangeRequestStore) setDelivery(id string, d *ChangeDelivery) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = cs.db.Exec(cs.q(`UPDATE change_requests SET delivery = ? WHERE id = ?`), string(data), id)
	return err
}

// changeRequests is the process's store, set when routes are registered,
// so a distribution can record an outcome (SetChangeRequestOutcome).
var changeRequests *ChangeRequestStore

// SetChangeRequestOutcome records what became of a delivered change
// request: applied (merged where it was delivered) or declined (closed
// there). by names who decided, as the distribution knows them.
func SetChangeRequestOutcome(id string, applied bool, by, reason string) error {
	if changeRequests == nil {
		return errors.New("change requests are not set up")
	}
	status := ChangeDeclined
	if applied {
		status = ChangeApplied
	}
	_, err := changeRequests.decide(id, status, "", by, reason, 0)
	return err
}

// --- the diff

// FieldChange is one field of a pipeline or node, before and after.
type FieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// NodeRef names a node.
type NodeRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// NodeChange is a node that stays, with what changed in it.
type NodeChange struct {
	NodeRef
	Changes []FieldChange `json:"changes"`
}

// PipelineDiff is what a proposed definition changes, in the pipeline's
// terms.
type PipelineDiff struct {
	Pipeline     []FieldChange `json:"pipeline,omitempty"`
	NodesAdded   []NodeRef     `json:"nodes_added,omitempty"`
	NodesRemoved []NodeRef     `json:"nodes_removed,omitempty"`
	NodesChanged []NodeChange  `json:"nodes_changed,omitempty"`
	EdgesAdded   []models.Edge `json:"edges_added,omitempty"`
	EdgesRemoved []models.Edge `json:"edges_removed,omitempty"`
}

// Empty reports whether nothing changes.
func (d *PipelineDiff) Empty() bool {
	return len(d.Pipeline)+len(d.NodesAdded)+len(d.NodesRemoved)+len(d.NodesChanged)+len(d.EdgesAdded)+len(d.EdgesRemoved) == 0
}

// diffIgnored are a pipeline's fields that are identity or bookkeeping,
// not its definition, and the graph, compared on its own below.
var diffIgnored = map[string]bool{
	"id": true, "pipeline_id": true, "org_id": true, "workspace_id": true, "source": true,
	"created_at": true, "updated_at": true, "webhook_token": true, "nodes": true, "edges": true,
}

func plain(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

func sameValue(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// isEmptyValue treats absent, null, "", [] and {} alike.
func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func fieldChanges(before, after map[string]any, ignored map[string]bool) []FieldChange {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		if !ignored[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var out []FieldChange
	for _, k := range names {
		b, a := before[k], after[k]
		if (isEmptyValue(b) && isEmptyValue(a)) || sameValue(b, a) {
			continue
		}
		out = append(out, FieldChange{Field: k, Before: b, After: a})
	}
	return out
}

// diffPipelines is what changes from current to proposed.
func diffPipelines(current, proposed *models.Pipeline) *PipelineDiff {
	d := &PipelineDiff{}
	cur, _ := plain(current).(map[string]any)
	next, _ := plain(proposed).(map[string]any)
	d.Pipeline = fieldChanges(cur, next, diffIgnored)

	before := map[string]models.Node{}
	for _, n := range current.Nodes {
		before[n.ID] = n
	}
	after := map[string]bool{}
	for _, n := range proposed.Nodes {
		after[n.ID] = true
		old, ok := before[n.ID]
		if !ok {
			d.NodesAdded = append(d.NodesAdded, NodeRef{ID: n.ID, Name: n.Name, Type: string(n.Type)})
			continue
		}
		var changes []FieldChange
		if old.Name != n.Name {
			changes = append(changes, FieldChange{Field: "name", Before: old.Name, After: n.Name})
		}
		if old.Type != n.Type {
			changes = append(changes, FieldChange{Field: "type", Before: old.Type, After: n.Type})
		}
		oc, _ := plain(old.Config).(map[string]any)
		nc, _ := plain(n.Config).(map[string]any)
		for _, c := range fieldChanges(oc, nc, nil) {
			c.Field = "config." + c.Field
			changes = append(changes, c)
		}
		if len(changes) > 0 {
			d.NodesChanged = append(d.NodesChanged, NodeChange{NodeRef: NodeRef{ID: n.ID, Name: n.Name, Type: string(n.Type)}, Changes: changes})
		}
	}
	for _, n := range current.Nodes {
		if !after[n.ID] {
			d.NodesRemoved = append(d.NodesRemoved, NodeRef{ID: n.ID, Name: n.Name, Type: string(n.Type)})
		}
	}
	key := func(e models.Edge) string { data, _ := json.Marshal(e); return string(data) }
	was, is := map[string]bool{}, map[string]bool{}
	for _, e := range current.Edges {
		was[key(e)] = true
	}
	for _, e := range proposed.Edges {
		is[key(e)] = true
		if !was[key(e)] {
			d.EdgesAdded = append(d.EdgesAdded, e)
		}
	}
	for _, e := range current.Edges {
		if !is[key(e)] {
			d.EdgesRemoved = append(d.EdgesRemoved, e)
		}
	}
	return d
}

// keepLayout gives each node of proposed that exists in current, and comes
// without a position, the position it has now. An agent works from the
// definition without its canvas layout (MCP leaves positions out), and
// applying its proposal must not stack every node at the origin.
func keepLayout(current, proposed *models.Pipeline) {
	at := map[string]models.Position{}
	for _, n := range current.Nodes {
		at[n.ID] = n.Position
	}
	for i, n := range proposed.Nodes {
		if pos, ok := at[n.ID]; ok && n.Position == (models.Position{}) {
			proposed.Nodes[i].Position = pos
		}
	}
}

// --- the API

// changeRequestsAPI serves change requests for the pipeline handler's
// store, scheduler and executors.
type changeRequestsAPI struct {
	ph *PipelineHandler
	e  *engine.Engine
	cs *ChangeRequestStore
	pc *PermissionChecker
}

func registerChangeRequestRoutes(r chi.Router, ph *PipelineHandler, e *engine.Engine, cs *ChangeRequestStore, pc *PermissionChecker,
	requirePerm func(models.Permission) func(http.Handler) http.Handler) {
	changeRequests = cs
	h := &changeRequestsAPI{ph: ph, e: e, cs: cs, pc: pc}
	r.With(requirePerm(models.PermPipelinesPropose)).Post("/pipelines/{id}/changes", h.create)
	r.Get("/changes", h.listWorkspace)
	r.Get("/pipelines/{id}/changes", h.list)
	r.Get("/pipelines/{id}/changes/{changeId}", h.get)
	r.With(requirePerm(models.PermPipelinesEdit)).Post("/pipelines/{id}/changes/{changeId}/apply", h.apply)
	r.Post("/pipelines/{id}/changes/{changeId}/decline", h.decline)
	r.With(requirePerm(models.PermPipelinesRun)).Post("/pipelines/{id}/changes/{changeId}/test", h.test)
}

// pipeline is the pipeline in the path when the caller may reach it.
func (h *changeRequestsAPI) pipeline(w http.ResponseWriter, r *http.Request) (*models.Pipeline, bool) {
	p, err := h.ph.store.GetPipeline(chi.URLParam(r, "id"))
	if err != nil || p == nil {
		writeError(w, http.StatusNotFound, "pipeline not found")
		return nil, false
	}
	if !ValidateOrgAccess(r, p.OrgID) {
		DenyOrgAccess(w)
		return nil, false
	}
	if !userOwnsWorkspace(r, p.WorkspaceID) {
		denyWorkspaceAccess(w)
		return nil, false
	}
	return p, true
}

// currentVersion is the pipeline's latest saved version.
func (h *changeRequestsAPI) currentVersion(pipelineID string) int {
	versions, err := h.ph.store.ListPipelineVersions(pipelineID)
	if err != nil {
		return 0
	}
	latest := 0
	for _, v := range versions {
		if v.Version > latest {
			latest = v.Version
		}
	}
	return latest
}

// who is the caller: id, name, kind, and whom an agent acts for.
func who(r *http.Request) (id, name, kind, onBehalfOf string) {
	claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
	if claims == nil {
		return "", "", UserKindPerson, ""
	}
	id, _ = (*claims)["sub"].(string)
	name, _ = (*claims)["display_name"].(string)
	if name == "" {
		name, _ = (*claims)["username"].(string)
	}
	kind = UserKindPerson
	if isAgentClaims(claims) {
		kind = UserKindAgent
		onBehalfOf, _ = (*claims)["on_behalf_of"].(string)
	}
	return id, name, kind, onBehalfOf
}

// present fills what is computed on read.
func (h *changeRequestsAPI) present(cr *ChangeRequest, p *models.Pipeline, current int, full bool) {
	cr.Stale = cr.Status == ChangeOpen && cr.BaseVersion != current
	if full {
		cr.Diff = diffPipelines(p, cr.Proposed)
	} else {
		cr.Proposed = nil
	}
}

func validateProposed(p *models.Pipeline, executors []extensions.NodeExecutor) ChangeValidation {
	v := ChangeValidation{Valid: true, Errors: []string{}}
	if err := p.Validate(); err != nil {
		v.Errors = append(v.Errors, err.Error())
	}
	if !p.Draft {
		if ve := engine.ValidatePipeline(p, executors...); ve.HasErrors() {
			v.Errors = append(v.Errors, ve.Errors...)
		}
	}
	v.Valid = len(v.Errors) == 0
	return v
}

func (h *changeRequestsAPI) create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pipeline(w, r)
	if !ok {
		return
	}
	if p.Source == models.PipelineSourceGit && ChangeRequestDeliverFunc == nil {
		writeError(w, http.StatusConflict, "this pipeline is deployed from a repository; propose the change there")
		return
	}
	var req struct {
		Pipeline    json.RawMessage `json:"pipeline"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil || len(req.Pipeline) == 0 {
		writeError(w, http.StatusBadRequest, `send {"pipeline": <the proposed definition>, "title": "..."}`)
		return
	}
	proposed, err := decodePipelineStrict(strings.NewReader(string(req.Pipeline)))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || len(title) > 200 {
		writeError(w, http.StatusBadRequest, "give the change a title (up to 200 characters)")
		return
	}
	// The proposal is a version of this pipeline: its identity is the
	// pipeline's, whatever the body said.
	proposed.ID, proposed.PipelineID, proposed.Source = p.ID, p.PipelineID, p.Source
	proposed.WorkspaceID, proposed.OrgID, proposed.CreatedAt = p.WorkspaceID, p.OrgID, p.CreatedAt
	proposed.WebhookToken = p.WebhookToken
	if proposed.Nodes == nil {
		proposed.Nodes = []models.Node{}
	}
	if proposed.Edges == nil {
		proposed.Edges = []models.Edge{}
	}
	keepLayout(p, proposed)
	diff := diffPipelines(p, proposed)
	if diff.Empty() {
		writeError(w, http.StatusUnprocessableEntity, "the proposed pipeline is the same as the current one")
		return
	}
	id, name, kind, onBehalfOf := who(r)
	cr := &ChangeRequest{
		ID: generateID(), PipelineID: p.ID, Title: title, Description: strings.TrimSpace(req.Description),
		Status: ChangeOpen, BaseVersion: h.currentVersion(p.ID), Proposed: proposed,
		Validation: validateProposed(proposed, h.ph.executors),
		CreatedBy:  id, CreatedByName: name, CreatedByKind: kind, OnBehalfOf: onBehalfOf, CreatedAt: time.Now().UTC(),
	}
	if err := h.cs.create(cr); err != nil {
		writeError(w, http.StatusInternalServerError, "could not record the change request")
		return
	}
	if p.Source == models.PipelineSourceGit {
		d, err := ChangeRequestDeliverFunc(r, cr, p)
		if err != nil {
			_, _ = h.cs.decide(cr.ID, ChangeDeclined, "", "", "could not be delivered: "+err.Error(), 0)
			writeError(w, http.StatusBadGateway, "the change request could not be delivered: "+err.Error())
			return
		}
		if d != nil {
			cr.Delivery = d
			_ = h.cs.setDelivery(cr.ID, d)
		}
	}
	AuditLog(r, "propose", "pipeline", p.ID, nil, map[string]interface{}{"change_request": cr.ID, "title": cr.Title})
	h.present(cr, p, cr.BaseVersion, true)
	writeJSON(w, http.StatusCreated, cr)
}

func (h *changeRequestsAPI) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pipeline(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != ChangeOpen && status != ChangeApplied && status != ChangeDeclined {
		writeError(w, http.StatusBadRequest, "status is open, applied or declined")
		return
	}
	list, err := h.cs.list(p.ID, status, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list change requests")
		return
	}
	current := h.currentVersion(p.ID)
	for i := range list {
		h.present(&list[i], p, current, false)
	}
	writeJSON(w, http.StatusOK, list)
}

// WorkspaceChange is a change request listed across pipelines, with its
// pipeline's name.
type WorkspaceChange struct {
	ChangeRequest
	PipelineName string `json:"pipeline_name"`
}

// listWorkspace is the change requests of the pipelines the caller may see
// in the workspace of the request, newest first (at most 100): what a
// person has to review, wherever it was proposed.
func (h *changeRequestsAPI) listWorkspace(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != ChangeOpen && status != ChangeApplied && status != ChangeDeclined {
		writeError(w, http.StatusBadRequest, "status is open, applied or declined")
		return
	}
	recent, err := h.cs.listRecent(status, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list change requests")
		return
	}
	ws := GetWorkspaceID(r)
	type seen struct {
		p       *models.Pipeline
		ok      bool
		current int
	}
	pipelines := map[string]*seen{}
	out := []WorkspaceChange{}
	for i := range recent {
		cr := recent[i]
		s := pipelines[cr.PipelineID]
		if s == nil {
			s = &seen{}
			if p, err := h.ph.store.GetPipeline(cr.PipelineID); err == nil && p != nil &&
				p.WorkspaceID == ws && ValidateOrgAccess(r, p.OrgID) && userOwnsWorkspace(r, p.WorkspaceID) {
				s.p, s.ok, s.current = p, true, h.currentVersion(p.ID)
			}
			pipelines[cr.PipelineID] = s
		}
		if !s.ok {
			continue
		}
		h.present(&cr, s.p, s.current, false)
		out = append(out, WorkspaceChange{ChangeRequest: cr, PipelineName: s.p.Name})
		if len(out) == 100 {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *changeRequestsAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pipeline(w, r)
	if !ok {
		return
	}
	cr, err := h.cs.get(p.ID, chi.URLParam(r, "changeId"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrChangeNotFound.Error())
		return
	}
	h.present(cr, p, h.currentVersion(p.ID), true)
	writeJSON(w, http.StatusOK, cr)
}

func (h *changeRequestsAPI) apply(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pipeline(w, r)
	if !ok {
		return
	}
	id, name, kind, _ := who(r)
	if kind == UserKindAgent {
		writeError(w, http.StatusForbidden, "an agent cannot apply a change request; a person does")
		return
	}
	cr, err := h.cs.get(p.ID, chi.URLParam(r, "changeId"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrChangeNotFound.Error())
		return
	}
	switch {
	case cr.Status != ChangeOpen:
		writeError(w, http.StatusConflict, "this change request is "+cr.Status)
		return
	case p.Source == models.PipelineSourceGit:
		writeError(w, http.StatusConflict, "this pipeline is deployed from a repository; the change is applied by merging it there")
		return
	case cr.BaseVersion != h.currentVersion(p.ID):
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"the pipeline has changed since this request was made (version %d); applying it would undo those changes. Decline it and propose again",
			cr.BaseVersion))
		return
	}
	message := "change request: " + cr.Title
	saved, status, msg := h.ph.saveUpdate(r, p, cr.Proposed, message)
	if status != http.StatusOK {
		writeError(w, status, "the proposed pipeline cannot be saved: "+msg)
		return
	}
	version := h.currentVersion(p.ID)
	if settled, err := h.cs.decide(cr.ID, ChangeApplied, id, name, "", version); err != nil || !settled {
		// Saved, but another request settled it at the same moment; the
		// save stands and is in the version history either way.
		writeError(w, http.StatusConflict, "the change was saved, but this request was settled meanwhile")
		return
	}
	AuditLog(r, "apply_change", "pipeline", p.ID, nil, map[string]interface{}{"change_request": cr.ID, "version": version})
	writeJSON(w, http.StatusOK, map[string]interface{}{"pipeline": saved, "version": version})
}

func (h *changeRequestsAPI) decline(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pipeline(w, r)
	if !ok {
		return
	}
	cr, err := h.cs.get(p.ID, chi.URLParam(r, "changeId"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrChangeNotFound.Error())
		return
	}
	id, name, _, _ := who(r)
	// Whoever may edit the pipeline declines; the author withdraws.
	if id != cr.CreatedBy && !h.pc.HasPermission(r, models.PermPipelinesEdit) {
		writeError(w, http.StatusForbidden, "insufficient permissions: "+string(models.PermPipelinesEdit))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req)
	settled, err := h.cs.decide(cr.ID, ChangeDeclined, id, name, strings.TrimSpace(req.Reason), 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not decline the change request")
		return
	}
	if !settled {
		writeError(w, http.StatusConflict, "this change request is "+cr.Status)
		return
	}
	AuditLog(r, "decline_change", "pipeline", p.ID, nil, map[string]interface{}{"change_request": cr.ID})
	w.WriteHeader(http.StatusNoContent)
}

// changeTests bounds the change-request tests running at once: each is two
// previews, which run in this process.
var changeTests = make(chan struct{}, 2)

// test previews the current pipeline and the proposed one on a few rows,
// so a person can see what the change does to the data before applying
// it. Nothing is written outside Brokoli and no run is recorded (see
// engine.PreviewPipeline). The proposal's code runs, which is why only a
// person starts a test, never an agent.
func (h *changeRequestsAPI) test(w http.ResponseWriter, r *http.Request) {
	p, ok := h.pipeline(w, r)
	if !ok {
		return
	}
	if _, _, kind, _ := who(r); kind == UserKindAgent {
		writeError(w, http.StatusForbidden, "an agent cannot test a change request: testing runs the proposed code, so a person starts it")
		return
	}
	if h.e == nil {
		writeError(w, http.StatusServiceUnavailable, "this server cannot run previews")
		return
	}
	cr, err := h.cs.get(p.ID, chi.URLParam(r, "changeId"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrChangeNotFound.Error())
		return
	}
	if !cr.Validation.Valid {
		writeError(w, http.StatusConflict, "the proposed pipeline is not valid, so it cannot be tested")
		return
	}
	select {
	case changeTests <- struct{}{}:
		defer func() { <-changeTests }()
	default:
		writeError(w, http.StatusTooManyRequests, "other tests are running; try again in a moment")
		return
	}
	var req struct {
		Rows int `json:"rows"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
	if req.Rows <= 0 || req.Rows > 50 {
		req.Rows = 10
	}
	// Both at once, so the test takes as long as the slower preview.
	var current *engine.Preview
	var currentErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		current, currentErr = h.e.PreviewPipeline(r.Context(), p, req.Rows)
	}()
	proposed, err := h.e.PreviewPipeline(r.Context(), cr.Proposed, req.Rows)
	<-done
	if currentErr != nil {
		writeError(w, http.StatusInternalServerError, "the current pipeline could not be previewed: "+currentErr.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the proposed pipeline could not be previewed: "+err.Error())
		return
	}
	AuditLog(r, "test_change", "pipeline", p.ID, nil, map[string]interface{}{"change_request": cr.ID})
	writeJSON(w, http.StatusOK, map[string]interface{}{"rows": req.Rows, "current": current, "proposed": proposed})
}
