package store

// Compile-time proof that the concrete stores satisfy both the composed
// Store and every narrow capability interface. If a future change drops a
// method, the failure names the exact capability (e.g. AlertStore) rather
// than a hundred-method blur — which is the point of the ADR-015 split.

var (
	_ Store = (*SQLiteStore)(nil)
	_ Store = (*PostgresStore)(nil)

	// Narrow capabilities — spot-checked across both backends so a
	// consumer can safely depend on just the slice it needs.
	_ PipelineStore    = (*SQLiteStore)(nil)
	_ RunStore         = (*SQLiteStore)(nil)
	_ NodeRunStore     = (*SQLiteStore)(nil)
	_ RunEventStore    = (*SQLiteStore)(nil)
	_ AlertStore       = (*SQLiteStore)(nil)
	_ DLQStore         = (*SQLiteStore)(nil)
	_ TemplateStore    = (*SQLiteStore)(nil)
	_ MaintenanceStore = (*SQLiteStore)(nil)
	_ TxStore          = (*SQLiteStore)(nil)

	_ PipelineStore    = (*PostgresStore)(nil)
	_ RunStore         = (*PostgresStore)(nil)
	_ AlertStore       = (*PostgresStore)(nil)
	_ MaintenanceStore = (*PostgresStore)(nil)

	// Optional capabilities (not embedded in Store): core backends
	// implement them, callers reach them by type assertion.
	_ PhysicalPlanStore = (*SQLiteStore)(nil)
	_ PhysicalPlanStore = (*PostgresStore)(nil)
)
