// Package templates defines the built-in pipeline templates offered when
// creating a new pipeline (ui/src/pages/Pipelines.svelte's "choose a
// template" screen). These used to be hardcoded as JS object literals in
// that file — never validated, never executed by anything, and free to
// silently drift out of sync with whatever config shape the engine
// actually expects. See TestBuiltinTemplates in api/templates_test.go,
// which runs every non-blank template here through the real engine
// against fixture data and asserts it produces the expected outcome —
// that test is the actual point of this migration, not just the storage
// location change.
package templates

import "github.com/Tnsor-Labs/brokoli/models"

// Template is a starter pipeline definition offered at pipeline-creation
// time. Nodes/Edges are copied as-is into a new models.Pipeline when a
// user picks one — see api/handlers_templates.go.
type Template struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Icon        string        `json:"icon"`
	Nodes       []models.Node `json:"nodes"`
	Edges       []models.Edge `json:"edges"`
}

// Builtin is the fixed set of templates shipped with every Brokoli
// instance. Order matters — it's the order they render in the UI's
// template picker.
var Builtin = []Template{
	{
		ID:          "blank",
		Name:        "Blank",
		Description: "Start from scratch",
		Icon:        "plus",
		Nodes:       []models.Node{},
		Edges:       []models.Edge{},
	},
	{
		ID:          "hello-world",
		Name:        "Hello World",
		Description: "Minimal: fetch, transform, save",
		Icon:        "file",
		Nodes: []models.Node{
			{
				ID:   "s1",
				Type: models.NodeTypeSourceAPI,
				Name: "Fetch Employees",
				Config: map[string]interface{}{
					"url":    "/api/samples/data/employees.json",
					"method": "GET",
				},
				Position: models.Position{X: 40, Y: 120},
			},
			{
				ID:   "t1",
				Type: models.NodeTypeTransform,
				Name: "Add Column",
				Config: map[string]interface{}{
					"rules": []map[string]interface{}{
						{"type": "add_column", "name": "greeting", "expression": "'Hello, ' + name"},
					},
				},
				Position: models.Position{X: 360, Y: 120},
			},
			{
				ID:   "o1",
				Type: models.NodeTypeSinkFile,
				Name: "Save Result",
				Config: map[string]interface{}{
					"path": "/tmp/hello-output.csv",
				},
				Position: models.Position{X: 680, Y: 120},
			},
		},
		Edges: []models.Edge{
			{From: "s1", To: "t1"},
			{From: "t1", To: "o1"},
		},
	},
	{
		ID:          "api-fetch",
		Name:        "API Fetch",
		Description: "Fetch, filter, save",
		Icon:        "api",
		Nodes: []models.Node{
			{
				ID:   "s1",
				Type: models.NodeTypeSourceAPI,
				Name: "Fetch Orders",
				Config: map[string]interface{}{
					"url":    "/api/samples/data/orders.json",
					"method": "GET",
				},
				Position: models.Position{X: 40, Y: 120},
			},
			{
				ID:   "t1",
				Type: models.NodeTypeTransform,
				Name: "Filter Completed",
				Config: map[string]interface{}{
					"rules": []map[string]interface{}{
						{"type": "filter", "condition": "status == 'completed'"},
					},
				},
				Position: models.Position{X: 360, Y: 120},
			},
			{
				ID:   "o1",
				Type: models.NodeTypeSinkFile,
				Name: "Save Orders",
				Config: map[string]interface{}{
					"path": "/tmp/completed-orders.csv",
				},
				Position: models.Position{X: 680, Y: 120},
			},
		},
		Edges: []models.Edge{
			{From: "s1", To: "t1"},
			{From: "t1", To: "o1"},
		},
	},
	{
		ID:          "join-aggregate",
		Name:        "Join + Aggregate",
		Description: "Join two sources, aggregate results",
		Icon:        "merge",
		Nodes: []models.Node{
			{
				ID:   "s1",
				Type: models.NodeTypeSourceAPI,
				Name: "Orders",
				Config: map[string]interface{}{
					"url":    "/api/samples/data/orders.json",
					"method": "GET",
				},
				Position: models.Position{X: 40, Y: 60},
			},
			{
				ID:   "s2",
				Type: models.NodeTypeSourceAPI,
				Name: "Products",
				Config: map[string]interface{}{
					"url":    "/api/samples/data/products.json",
					"method": "GET",
				},
				Position: models.Position{X: 40, Y: 220},
			},
			{
				ID:   "j1",
				Type: models.NodeTypeJoin,
				Name: "Join",
				Config: map[string]interface{}{
					"join_type": "inner",
					"left_key":  "product",
					"right_key": "name",
				},
				Position: models.Position{X: 360, Y: 140},
			},
			{
				ID:   "t1",
				Type: models.NodeTypeTransform,
				Name: "Aggregate",
				Config: map[string]interface{}{
					"rules": []map[string]interface{}{
						{
							"type":     "aggregate",
							"group_by": []string{"product"},
							"agg_fields": []map[string]interface{}{
								{"column": "total", "function": "sum", "alias": "total_revenue"},
							},
						},
					},
				},
				Position: models.Position{X: 680, Y: 140},
			},
			{
				ID:   "o1",
				Type: models.NodeTypeSinkFile,
				Name: "Summary",
				Config: map[string]interface{}{
					"path": "/tmp/product-summary.csv",
				},
				Position: models.Position{X: 1000, Y: 140},
			},
		},
		Edges: []models.Edge{
			{From: "s1", To: "j1"},
			{From: "s2", To: "j1"},
			{From: "j1", To: "t1"},
			{From: "t1", To: "o1"},
		},
	},
	{
		ID:          "data-quality",
		Name:        "Data Quality",
		Description: "Validate data with quality gates",
		Icon:        "code",
		Nodes: []models.Node{
			{
				ID:   "s1",
				Type: models.NodeTypeSourceAPI,
				Name: "Fetch Employees",
				Config: map[string]interface{}{
					"url":    "/api/samples/data/employees.json",
					"method": "GET",
				},
				Position: models.Position{X: 40, Y: 120},
			},
			{
				ID:   "q1",
				Type: models.NodeTypeQualityCheck,
				Name: "Quality Gate",
				Config: map[string]interface{}{
					"rules": []map[string]interface{}{
						{"column": "email", "rule": "not_null", "on_failure": "block"},
						{"column": "salary", "rule": "min", "params": map[string]interface{}{"min": 0}, "on_failure": "warn"},
					},
				},
				Position: models.Position{X: 360, Y: 120},
			},
			{
				ID:   "t1",
				Type: models.NodeTypeTransform,
				Name: "Clean Data",
				Config: map[string]interface{}{
					"rules": []map[string]interface{}{
						{"type": "rename", "mapping": map[string]string{"hire_date": "start_date"}},
					},
				},
				Position: models.Position{X: 680, Y: 120},
			},
			{
				ID:   "o1",
				Type: models.NodeTypeSinkFile,
				Name: "Output",
				Config: map[string]interface{}{
					"path": "/tmp/clean-employees.csv",
				},
				Position: models.Position{X: 1000, Y: 120},
			},
		},
		Edges: []models.Edge{
			{From: "s1", To: "q1"},
			{From: "q1", To: "t1"},
			{From: "t1", To: "o1"},
		},
	},
}
