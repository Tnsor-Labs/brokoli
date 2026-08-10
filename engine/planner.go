package engine

// The physical planner (ADR-015, #90 M2): turn a logical pipeline into
// the physical plan the engine would schedule. This slice produces the
// plan on demand for explanation before execution — it does not persist
// instances or drive dispatch. It mirrors the runner's real semantics
// so the explanation is truthful: stages are the same Kahn waves the
// runner executes in, and each node's work-unit shape is read from the
// same config the handlers read (pagination execution policy, expansion
// blocks).

import (
	"fmt"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// PlanPipeline computes the physical plan for a pipeline. It returns an
// error only when the graph can't be staged (a cycle) — the executable
// validator catches that earlier at save time, but the planner must not
// loop on a bad graph reached another way.
func PlanPipeline(p *models.Pipeline) (*models.PhysicalPlan, error) {
	waves, err := topoWaves(p.Nodes, p.Edges)
	if err != nil {
		return nil, err
	}
	plan := &models.PhysicalPlan{PipelineID: p.PipelineID, IRVersion: p.IRVersion}
	for i, wave := range waves {
		stage := models.PhysicalStage{Index: i}
		for _, n := range wave {
			unit := planNode(n)
			stage.WorkUnits = append(stage.WorkUnits, unit)
			if unit.RuntimeResolved {
				plan.DynamicNodes++
			} else {
				plan.StaticInstanceCount += unit.StaticInstanceCount
			}
		}
		plan.Stages = append(plan.Stages, stage)
	}
	return plan, nil
}

// planNode classifies one logical node's physical shape from its config,
// reading exactly the keys the runtime handlers read.
func planNode(n models.Node) models.PhysicalWorkUnit {
	u := models.PhysicalWorkUnit{
		LogicalNodeID:       n.ID,
		NodeType:            string(n.Type),
		Kind:                models.WorkUnitSingle,
		InstanceKeyTemplate: n.ID,
		StaticInstanceCount: 1,
		RetryScope:          models.RetryScopeNode,
		Explain:             "one instance, retried as a whole node",
	}

	// Dynamic expansion: a code node carrying an `expansion` block fans
	// out to one instance per item in the resolved upstream collection.
	if exp, ok := mapField(n.Config, "expansion"); ok {
		u.Kind = models.WorkUnitExpansion
		u.RuntimeResolved = true
		u.StaticInstanceCount = 0
		u.RetryScope = models.RetryScopeWorkUnit
		u.InstanceKeyTemplate = n.ID + "[<item>]"
		over := "upstream collection"
		if m, ok := mapField(exp, "over"); ok {
			over = fmt.Sprintf("%d input(s): %v", len(m), sortedKeys(m))
		}
		u.ConcurrencyGroup = n.ID
		if mi, ok := intField(exp, "max_instances"); ok && mi > 0 {
			u.MaxConcurrency = mi
			u.Explain = fmt.Sprintf("fans out over %s at runtime; ≤%d concurrent instances, each retried independently", over, mi)
		} else {
			u.Explain = fmt.Sprintf("fans out over %s at runtime; each instance retried independently", over)
		}
		return u
	}

	// Paginated source: pages are fetched as bounded, page-retried work
	// within the node. Page count is runtime-resolved.
	if _, ok := mapField(n.Config, "pagination"); ok {
		u.Kind = models.WorkUnitPagination
		u.RuntimeResolved = true
		u.StaticInstanceCount = 0
		u.RetryScope = models.RetryScopeWorkUnit
		u.InstanceKeyTemplate = n.ID + "#page-<n>"
		u.ConcurrencyGroup = n.ID
		u.MaxConcurrency = 1 // matches rest_fetcher's default max_concurrency
		if exec, ok := mapField(n.Config, "execution"); ok {
			if mc, ok := intField(exec, "max_concurrency"); ok && mc > 0 {
				u.MaxConcurrency = mc
			}
		}
		u.Explain = fmt.Sprintf("fetches pages at runtime; ≤%d concurrent, each page retried independently", u.MaxConcurrency)
		return u
	}

	return u
}

// ProjectRunInstances returns the physical instances that executed in a
// run, projected from the records the run already keeps (ADR-015 §8, #90:
// "physical stages/instances on demand"). Plain nodes contribute one
// `single` instance each; a dynamic-expansion node contributes one
// `expansion` instance per resolved item — the node's own NodeRun stays
// the aggregate summary and is not itself emitted as an instance.
//
// It reads through the optional ExpansionInstanceStore capability; a
// store without it still projects the single-node instances. Pages of a
// paginated source are not individually recorded yet, so such a node
// projects as one single instance — honest about what's persisted.
func ProjectRunInstances(s store.Store, runID string) ([]models.PhysicalInstance, error) {
	nodeRuns, err := s.ListNodeRunsByRun(runID)
	if err != nil {
		return nil, err
	}

	// Which nodes fanned out — those have expansion_instances rows and
	// their per-item instances replace the single-node projection.
	expansionByNode := map[string][]models.ExpansionInstance{}
	if es, ok := s.(store.ExpansionInstanceStore); ok {
		items, err := es.ListExpansionInstancesByRun(runID)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			expansionByNode[it.NodeID] = append(expansionByNode[it.NodeID], it)
		}
	}

	var out []models.PhysicalInstance
	for _, nr := range nodeRuns {
		if _, fanned := expansionByNode[nr.NodeID]; fanned {
			continue // fan-out node: its instances come from the expansion rows
		}
		out = append(out, models.PhysicalInstance{
			LogicalNodeID: nr.NodeID,
			Kind:          models.WorkUnitSingle,
			InstanceKey:   nr.NodeID,
			Index:         0,
			Status:        nr.Status,
			RowCount:      nr.RowCount,
			StartedAt:     nr.StartedAt,
			DurationMs:    nr.DurationMs,
			Error:         nr.Error,
			Attempt:       nr.Attempt,
		})
	}
	for nodeID, items := range expansionByNode {
		for _, it := range items {
			key := it.InstanceKey
			if key == "" {
				key = fmt.Sprintf("%s[%d]", nodeID, it.InstanceIndex)
			}
			out = append(out, models.PhysicalInstance{
				LogicalNodeID: nodeID,
				Kind:          models.WorkUnitExpansion,
				InstanceKey:   key,
				Index:         it.InstanceIndex,
				Status:        it.Status,
				RowCount:      it.RowCount,
				StartedAt:     it.StartedAt,
				DurationMs:    it.DurationMs,
				Error:         it.Error,
				Attempt:       it.NodeAttempt,
			})
		}
	}
	return out, nil
}

// topoWaves groups nodes into dependency waves (Kahn by level): every
// node in wave i has all its predecessors in waves < i. This is the same
// wave structure the runner schedules in, so stage boundaries in the
// plan match real execution order.
func topoWaves(nodes []models.Node, edges []models.Edge) ([][]models.Node, error) {
	byID := make(map[string]models.Node, len(nodes))
	indeg := make(map[string]int, len(nodes))
	adj := make(map[string][]string)
	for _, n := range nodes {
		byID[n.ID] = n
		indeg[n.ID] = 0
	}
	for _, e := range edges {
		if _, ok := byID[e.From]; !ok {
			continue
		}
		if _, ok := byID[e.To]; !ok {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
	}

	// Preserve authored order within a wave for stable, readable plans.
	var current []string
	for _, n := range nodes {
		if indeg[n.ID] == 0 {
			current = append(current, n.ID)
		}
	}

	var waves [][]models.Node
	placed := 0
	for len(current) > 0 {
		wave := make([]models.Node, 0, len(current))
		var next []string
		for _, id := range current {
			wave = append(wave, byID[id])
			placed++
		}
		// Decrement successors after the whole wave, so a node only drops
		// into the next wave once every predecessor in this wave is done.
		for _, id := range current {
			for _, succ := range adj[id] {
				indeg[succ]--
				if indeg[succ] == 0 {
					next = append(next, succ)
				}
			}
		}
		waves = append(waves, wave)
		current = next
	}
	if placed < len(nodes) {
		return nil, fmt.Errorf("pipeline contains a cycle; cannot compute a physical plan")
	}
	return waves, nil
}

// --- small typed config accessors (node config is map[string]any) ---

func mapField(cfg map[string]interface{}, key string) (map[string]interface{}, bool) {
	if cfg == nil {
		return nil, false
	}
	m, ok := cfg[key].(map[string]interface{})
	return m, ok
}

func intField(cfg map[string]interface{}, key string) (int, bool) {
	if cfg == nil {
		return 0, false
	}
	switch v := cfg[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64: // JSON numbers decode to float64
		return int(v), true
	}
	return 0, false
}

func sortedKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// small maps; insertion into a slice + simple sort keeps output stable
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
