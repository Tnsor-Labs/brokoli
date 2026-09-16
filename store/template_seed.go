package store

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/templates"
)

// templateSeedMarkerKey names the setting that records which
// pkg/templates.Builtin rows this database has ever been seeded with.
//
// The old rule was "seed only when pipeline_templates is empty", which
// made a template added to Builtin after an install's first migrate
// unreachable forever: no upgrade could deliver it, because the table
// was no longer empty. A real deployment sat on two starters for a
// month while Builtin grew to four, and reinstalling was the only cure.
//
// Recording the ids instead separates the two questions that the
// row-count conflated: "has this database ever been offered this
// template" (the marker) and "does it have the row right now" (the
// table). A template an admin deleted keeps its id in the marker, so it
// stays deleted; a template that never appeared in the marker is new,
// so it is inserted.
const templateSeedMarkerKey = "pipeline_templates.seeded_ids"

// seedBuiltinTemplates inserts the built-in templates this database has
// not been offered before, and records what it has been offered. It is
// shared by both backends through their own accessors so the two cannot
// drift apart.
//
// A database with no marker is either brand new (empty table, seed
// everything) or one seeded before markers existed. In the second case
// the rows present are taken as already offered, which is the best
// available evidence: nothing recorded what an older build inserted.
// The one consequence, deliberate and one-time, is that a built-in an
// admin deleted before this change ships comes back on the first
// migrate after it. From then on the marker is authoritative and
// deletions persist.
func seedBuiltinTemplates(
	getSetting func(key string) (string, error),
	setSetting func(key, value string) error,
	listTemplates func() ([]models.PipelineTemplate, error),
	createTemplate func(t *models.PipelineTemplate) error,
) error {
	marker, err := getSetting(templateSeedMarkerKey)
	if err != nil {
		return fmt.Errorf("read template seed marker: %w", err)
	}
	existing, err := listTemplates()
	if err != nil {
		return fmt.Errorf("list pipeline templates: %w", err)
	}
	present := make(map[string]bool, len(existing))
	for _, t := range existing {
		present[t.ID] = true
	}

	offered := parseSeedMarker(marker)
	firstMarker := marker == ""
	if firstMarker {
		for id := range present {
			offered[id] = true
		}
	}

	now := time.Now().UTC()
	changed := firstMarker
	for _, t := range templates.Builtin {
		if offered[t.ID] {
			continue
		}
		// Present but unrecorded means an admin recreated it under the
		// built-in id. Record it, never insert over it.
		if !present[t.ID] {
			tmpl := t
			tmpl.CreatedAt, tmpl.UpdatedAt = now, now
			if err := createTemplate(&tmpl); err != nil {
				return fmt.Errorf("seed template %q: %w", t.ID, err)
			}
		}
		offered[t.ID] = true
		changed = true
	}

	if !changed {
		return nil
	}
	if err := setSetting(templateSeedMarkerKey, formatSeedMarker(offered)); err != nil {
		return fmt.Errorf("record template seed marker: %w", err)
	}
	return nil
}

// parseSeedMarker reads the comma-separated id list. Unknown and
// duplicate ids are kept as-is: an id that left Builtin must still count
// as offered, or dropping and restoring a template would resurrect it.
func parseSeedMarker(marker string) map[string]bool {
	ids := make(map[string]bool)
	for _, raw := range strings.Split(marker, ",") {
		if id := strings.TrimSpace(raw); id != "" {
			ids[id] = true
		}
	}
	return ids
}

// formatSeedMarker writes Builtin's ids in Builtin's own order, then any
// remaining id sorted, so the stored value is stable across restarts and
// comparable in tests.
func formatSeedMarker(ids map[string]bool) string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, t := range templates.Builtin {
		if ids[t.ID] {
			out = append(out, t.ID)
			seen[t.ID] = true
		}
	}
	rest := make([]string, 0, len(ids))
	for id := range ids {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	return strings.Join(append(out, rest...), ",")
}
