package tenancy

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Spec says how a resource's workspace is found. Build one with Direct or
// Via; the zero value is not registrable.
type Spec struct {
	table string
	// Direct: the columns on the resource's own table.
	orgCol, workspaceCol string
	// Via: the column naming the parent row, and the parent's resource.
	parentCol, parent string
	// allowNull: a row with no parent belongs to the organization, not a
	// workspace, and is visible to every workspace in it.
	allowNull bool
	// key is the column that identifies a row; "id" unless KeyedBy.
	key string
}

// Direct is a resource that carries its own organization and workspace
// columns, as pipelines do.
func Direct(table, orgCol, workspaceCol string) Spec {
	return Spec{table: table, orgCol: orgCol, workspaceCol: workspaceCol}
}

// Via is a resource that belongs to the workspace of its parent row, as a
// run belongs to its pipeline's. The parent must be registered as Direct.
func Via(table, parentCol, parent string) Spec {
	return Spec{table: table, parentCol: parentCol, parent: parent}
}

// AllowNull marks rows with no parent as the organization's: visible in
// every workspace of it (an alert about no particular pipeline). A row
// whose parent no longer exists is still invisible, since its workspace
// cannot be established.
func (s Spec) AllowNull() Spec { s.allowNull = true; return s }

// KeyedBy names the column that identifies a row, for a table whose key
// is not "id" (one keyed by its pipeline, for instance).
func (s Spec) KeyedBy(col string) Spec { s.key = col; return s }

func (s Spec) keyCol() string {
	if s.key == "" {
		return "id"
	}
	return s.key
}

var (
	mu       sync.RWMutex
	registry = map[string]Spec{}
	ident    = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// Register declares a resource. Names and columns are SQL identifiers
// chosen in code, never input; they are checked here anyway, since they
// are interpolated into queries.
func Register(name string, s Spec) {
	for _, id := range []string{name, s.table, s.orgCol, s.workspaceCol, s.parentCol, s.parent, s.key} {
		if id != "" && !ident.MatchString(id) {
			panic(fmt.Sprintf("tenancy: %q is not a plain SQL identifier", id))
		}
	}
	if s.table == "" || (s.workspaceCol == "") == (s.parentCol == "") {
		panic(fmt.Sprintf("tenancy: %s must be Direct or Via", name))
	}
	mu.Lock()
	defer mu.Unlock()
	if s.parent != "" {
		p, ok := registry[s.parent]
		if !ok || p.workspaceCol == "" {
			panic(fmt.Sprintf("tenancy: %s's parent %s must be registered as Direct first", name, s.parent))
		}
	}
	registry[name] = s
}

// Resources lists the registered names, for the isolation test that
// covers every one of them.
func Resources() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Table is the table a registered resource's rows live in, for tests that
// exercise every resource generically.
func Table(name string) string {
	mu.RLock()
	defer mu.RUnlock()
	return registry[name].table
}

func lookup(name string) (Spec, Spec, error) {
	mu.RLock()
	defer mu.RUnlock()
	s, ok := registry[name]
	if !ok {
		return Spec{}, Spec{}, fmt.Errorf("tenancy: %s is not a registered resource", name)
	}
	if s.parent == "" {
		return s, s, nil
	}
	return s, registry[s.parent], nil
}

// OwnerQuery is the SQL that answers the organization and workspace of one
// row of resource, by id, with one placeholder for the id. No row means
// the row does not exist, or its parent does not: invisible either way.
func OwnerQuery(resource string) (string, error) {
	s, root, err := lookup(resource)
	if err != nil {
		return "", err
	}
	if s.parent == "" {
		return fmt.Sprintf(`SELECT %s, %s FROM %s WHERE %s = ?`, s.orgCol, s.workspaceCol, s.table, s.keyCol()), nil
	}
	if s.allowNull {
		return fmt.Sprintf(`SELECT COALESCE(p.%s, ''), COALESCE(p.%s, '') FROM %s c LEFT JOIN %s p ON p.id = c.%s
			WHERE c.%s = ? AND (p.id IS NOT NULL OR c.%s IS NULL OR c.%s = '')`,
			root.orgCol, root.workspaceCol, s.table, root.table, s.parentCol, s.keyCol(), s.parentCol, s.parentCol), nil
	}
	return fmt.Sprintf(`SELECT p.%s, p.%s FROM %s c JOIN %s p ON p.id = c.%s WHERE c.%s = ?`,
		root.orgCol, root.workspaceCol, s.table, root.table, s.parentCol, s.keyCol()), nil
}

// ListFilter is the condition that keeps a list of resource to what this
// scope lists, for a query whose table is aliased alias (or unaliased when
// empty), with ? placeholders. It is "1=1" for an unrestricted scope and
// "1=0" for an invalid one, so it can always be ANDed in.
func (s Scope) ListFilter(resource, alias string) (string, []any, error) {
	spec, root, err := lookup(resource)
	if err != nil {
		return "", nil, err
	}
	if !s.valid {
		return "1=0", nil, nil
	}
	if !s.tenanted {
		return "1=1", nil, nil
	}
	col := func(c string) string {
		if alias == "" {
			return c
		}
		return alias + "." + c
	}
	// Without a workspace registry only the organization is kept to.
	where, args := fmt.Sprintf("%s = ? AND %s = ?", root.orgCol, root.workspaceCol), []any{s.OrgID, s.Workspace}
	if !s.byWS {
		where, args = root.orgCol+" = ?", []any{s.OrgID}
	}
	if spec.parent == "" {
		direct := fmt.Sprintf("(%s = ? AND %s = ?)", col(spec.orgCol), col(spec.workspaceCol))
		if !s.byWS {
			direct = fmt.Sprintf("(%s = ?)", col(spec.orgCol))
		}
		return direct, args, nil
	}
	in := fmt.Sprintf("%s IN (SELECT id FROM %s WHERE %s)", col(spec.parentCol), root.table, where)
	if spec.allowNull {
		return fmt.Sprintf("(%s IS NULL OR %s = '' OR %s)", col(spec.parentCol), col(spec.parentCol), in), args, nil
	}
	return "(" + in + ")", args, nil
}
