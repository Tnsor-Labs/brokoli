package api

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// The gate of ADR-049: handlers reach tenant data through the request's
// scope (scopedStore), not the raw store. The raw store answers for every
// organization and workspace, so a handler calling it directly is one
// forgotten check away from a leak, which is how every leak the ADR lists
// happened.
//
// This walks package api and fails on a call to a tenant-data method of
// the raw store (the interfaces below) from a function not on the
// allowlist. The allowlist is today's remaining call sites, each with how
// many calls it makes; it may only shrink. A stale entry fails too, so a
// migrated site is taken off the list in the same change.

// tenantInterfaces are the store interfaces whose rows belong to a
// workspace. A method declared on one of them is tenant data.
var tenantInterfaces = map[string]bool{
	"PipelineStore": true,
	"RunStore":      true,
	"DLQStore":      true,
	"AlertStore":    true,
}

// rawTenantCallAllowlist is "file: function: Method" -> number of calls.
// Every entry is a site still to move onto the scoped store, or a system
// caller with no request scope; the reason is in the comment.
var rawTenantCallAllowlist = map[string]int{
	// System callers, with no request scope to use.
	// Trigger dispatch runs from the event bus, not a request; it checks
	// the agent's own permissions on what it reaches.
	"agent_trigger_dispatch.go: deliveryRefusal: GetPipeline":        1,
	"agent_trigger_dispatch.go: followsAFailure: ListRunsByPipeline": 1,
	"agent_trigger_dispatch.go: match: GetPipeline":                  1,
	"agent_trigger_dispatch.go: match: GetRun":                       1,
	"agent_trigger_raise.go: raise: GetPipeline":                     1,
	"agent_trigger_raise.go: raise: GetRun":                          1,
	// Health probe: a store round trip; no tenant data is returned.
	"observability.go: HealthHandler: ListPipelines": 1,
	// A webhook is authenticated by the pipeline's own token, not a
	// person, so there is no request scope.
	"handlers_misc.go: webhookTriggerHandler: GetPipeline": 1,
	// The ids come from the scoped list just before; fetching each
	// through the scope again would double the reads.
	"handlers_run_started_by.go: ListStartedBy: GetRun": 1,

	// Still to move onto the scoped store. Each one checks the scope
	// by hand today, before or after the raw call.
	"agent_messages_handlers.go: mayRead: GetPipeline":                        1,
	"agent_messages_handlers.go: resolveLinks: GetPipeline":                   1,
	"agent_messages_handlers.go: resolveLinks: GetRun":                        1,
	"agent_trigger_views.go: runNotices: GetRun":                              1,
	"agent_trigger_views.go: visiblePipeline: GetPipeline":                    1,
	"agent_triggers_handlers.go: check: GetPipeline":                          1,
	"agent_triggers_handlers.go: testPipeline: GetPipeline":                   1,
	"agent_triggers_handlers.go: testPipeline: ListPipelines":                 1,
	"change_requests.go: listWorkspace: GetPipeline":                          1,
	"change_requests.go: pipeline: GetPipeline":                               1,
	"handlers_misc.go: dashboardHandler: ListPipelinesByOrgAndWorkspace":      1,
	"handlers_misc.go: dashboardHandler: ListPipelinesByWorkspace":            1,
	"handlers_misc.go: dashboardHandler: ListRunsByPipeline":                  1,
	"handlers_misc.go: pipelineDependencyGraphHandler: ListPipelineDepsByOrg": 1,
	"handlers_misc.go: pipelineDependentsHandler: GetPipeline":                1,
	"handlers_misc.go: pipelineDependentsHandler: ListPipelineDepsByOrg":      1,
	"handlers_misc.go: pipelineDepsHandler: GetPipeline":                      1,
	"handlers_misc.go: pipelineSummaryHandler: ListRunsByPipeline":            1,
	"handlers_pipeline.go: Clone: CreatePipeline":                             1,
	"handlers_pipeline.go: Clone: GetPipeline":                                1,
	"handlers_pipeline.go: Create: CreatePipeline":                            1,
	"handlers_pipeline.go: Delete: GetPipeline":                               1,
	"handlers_pipeline.go: commitDelete: DeletePipelineTx":                    2,
	"handlers_pipeline.go: commitDelete: GetPipeline":                         1,
	"handlers_pipeline.go: commitDelete: UpdatePipelineTx":                    1,
	"handlers_pipeline.go: dependentsOf: ListPipelineDepsByOrg":               1,
	"handlers_pipeline.go: Export: GetPipeline":                               1,
	"handlers_pipeline.go: Get: GetPipeline":                                  1,
	"handlers_pipeline.go: Import: CreatePipeline":                            1,
	"handlers_pipeline.go: List: ListPipelinesByOrgAndWorkspaceCursor":        1,
	"handlers_pipeline.go: List: ListPipelinesByWorkspace":                    1,
	"handlers_pipeline.go: ListVersions: GetPipeline":                         1,
	"handlers_pipeline.go: Plan: GetPipeline":                                 1,
	"handlers_pipeline.go: Rollback: GetPipeline":                             1,
	"handlers_pipeline.go: Rollback: UpdatePipeline":                          1,
	"handlers_pipeline.go: Update: GetPipeline":                               1,
	"handlers_pipeline.go: Validate: GetPipeline":                             1,
	"handlers_pipeline.go: ValidateNodes: GetPipeline":                        1,
	"handlers_pipeline.go: saveUpdate: UpdatePipeline":                        1,
	"handlers_pipeline.go: validateDependencyOrgScope: ListPipelineDepsByOrg": 1,
	"routes.go: RegisterRoutes: GetPipeline":                                  1,
	"routes.go: RegisterRoutes: GetPipelineByPipelineIDInOrg":                 1,
	"routes.go: RegisterRoutes: GetPipelineByPipelineIDInWorkspace":           1,
}

func TestHandlersReachTenantDataThroughTheScope(t *testing.T) {
	// A static check of the source: the race detector has nothing to find
	// in it, and type-checking the module inside a race-instrumented
	// binary took a minute and a half of the race run's budget. CI's
	// coverage pass runs it without -race.
	if raceEnabled {
		t.Skip("static source check; runs in the non-race pass")
	}
	found, files := rawTenantCalls(t, ".")
	if files < 50 {
		t.Fatalf("the gate read %d files of package api; it is not looking at the handlers", files)
	}
	var unexpected, stale []string
	for key, n := range found {
		if allowed := rawTenantCallAllowlist[key]; n > allowed {
			unexpected = append(unexpected, fmt.Sprintf("%s (%d calls, %d allowed)", key, n, allowed))
		}
	}
	for key, allowed := range rawTenantCallAllowlist {
		if found[key] < allowed {
			stale = append(stale, fmt.Sprintf("%s (%d allowed, %d left)", key, allowed, found[key]))
		}
	}
	sort.Strings(unexpected)
	sort.Strings(stale)
	for _, u := range unexpected {
		t.Errorf("raw store call to tenant data: %s; use scopedStore(s, r), or the system store with a reason", u)
	}
	for _, s := range stale {
		t.Errorf("allowlist entry no longer needed: %s; lower or delete it", s)
	}
	if os.Getenv("TENANCY_GATE_PRINT") != "" {
		keys := make([]string, 0, len(found))
		for k := range found {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("\t%q: %d,\n", k, found[k])
		}
	}
}

// rawTenantCalls counts, per "file: function: Method", the calls in the
// package at dir (test files excluded) to a method declared on one of
// tenantInterfaces, and how many files it read.
func rawTenantCalls(t *testing.T, dir string) (map[string]int, int) {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  dir,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatal(err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatal("package api did not load")
	}
	found := map[string]int{}
	files := 0
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			name := filepath.Base(p.Fset.Position(f.Pos()).Filename)
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			files++
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if method, ok := tenantMethod(p.TypesInfo.Uses[sel.Sel]); ok {
						found[fmt.Sprintf("%s: %s: %s", name, fn.Name.Name, method)]++
					}
					return true
				})
			}
		}
	}
	return found, files
}

// tenantMethod reports whether obj is a method declared on one of the
// store's tenant-data interfaces, and its name.
func tenantMethod(obj types.Object) (string, bool) {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Name() != "store" {
		return "", false
	}
	recv := fn.Type().(*types.Signature).Recv()
	if recv == nil {
		return "", false
	}
	named, ok := recv.Type().(*types.Named)
	if !ok || !tenantInterfaces[named.Obj().Name()] {
		return "", false
	}
	return fn.Name(), true
}
