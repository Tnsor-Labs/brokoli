package models

import "time"

// Alert is a persisted, readable notification. Before this existed, every
// alerting path in the product was fire-and-forget outbound (Slack webhooks,
// the notify node) — nothing was stored, so nothing could be read back. If
// you weren't watching Slack when a pipeline failed, no surface in the
// product would tell you it had.
//
// Alerts are org-scoped: an alert belongs to exactly one organization and
// must never be readable across tenants.
type Alert struct {
	ID       string `json:"id"`
	OrgID    string `json:"org_id"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`

	Title string `json:"title"`
	Body  string `json:"body,omitempty"`

	// PipelineID/RunID are optional back-references, letting the UI link an
	// alert straight to the thing that produced it.
	PipelineID   string `json:"pipeline_id,omitempty"`
	PipelineName string `json:"pipeline_name,omitempty"`
	RunID        string `json:"run_id,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
}

// Alert kinds. Deliberately an open set rather than a closed enum — new
// alert sources (including ones outside this repository) can define their
// own kind without a schema migration or a change here.
const (
	AlertKindRunFailure = "run_failure"
)

// Alert severities.
const (
	AlertSeverityInfo     = "info"
	AlertSeverityWarning  = "warning"
	AlertSeverityCritical = "critical"
)

// IsRead reports whether the alert has been marked read.
func (a *Alert) IsRead() bool { return a.ReadAt != nil }

// IsDismissed reports whether the alert has been dismissed. Dismissed alerts
// are excluded from the default listing but are not deleted, so a dismissal
// can be audited after the fact.
func (a *Alert) IsDismissed() bool { return a.DismissedAt != nil }
