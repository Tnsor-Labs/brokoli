package engine

import "github.com/hc12r/broked/models"

// ValidTransitions defines allowed state transitions for runs and nodes.
var ValidTransitions = map[models.RunStatus][]models.RunStatus{
	models.RunStatusPending:   {models.RunStatusRunning, models.RunStatusCancelled},
	models.RunStatusRunning:   {models.RunStatusSuccess, models.RunStatusFailed, models.RunStatusCancelled},
	models.RunStatusSuccess:   {},
	models.RunStatusFailed:    {},
	models.RunStatusCancelled: {},
}

// CanTransition checks if moving from one status to another is valid.
func CanTransition(from, to models.RunStatus) bool {
	allowed, ok := ValidTransitions[from]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}
