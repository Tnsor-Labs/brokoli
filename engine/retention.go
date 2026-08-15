package engine

import (
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Scheduled run retention (Tnsor-Labs/brokoli#214).
//
// store.PurgeRunsOlderThan existed and worked — node runs and events
// cascade with their run — but its only callers were the on-demand
// maintenance API endpoints, so a production deployment accumulated run
// history forever unless an operator remembered to call them (the 3-hour
// soak measured ~4MB/hour of database growth at modest load, linear).
// This sweep is the automation: opt-in via BROKOLI_RUN_RETENTION_DAYS
// (see cmd/serve.go), keep-forever by default, exactly today's behavior.
//
// Artifacts are deleted BEFORE the rows: ListRunIDsOlderThan mirrors the
// purge's WHERE clause precisely so each expiring run's artifacts —
// database rows and blobs on the SQL store, the whole per-run directory
// on the local-disk store — go with it. An artifact-delete failure is
// logged and does not block the row purge: a leaked artifact costs disk
// and is visible in logs, while a retention sweep that wedges on one bad
// run silently stops retention for everything.
//
// Safe on every instance concurrently: the purge is one conditional bulk
// DELETE, artifact deletes are idempotent (a no-op when already gone),
// and two sweeps racing just means one of them deletes nothing.
const DefaultRetentionSweepInterval = 6 * time.Hour

// retentionFirstSweepDelay is how long after startup the first sweep
// runs — long enough to stay out of startup recovery's way, short enough
// that an operator enabling retention sees it act without waiting a full
// interval.
var retentionFirstSweepDelay = 2 * time.Minute

// StartRunRetentionSweep launches the background retention sweep. days <= 0
// disables it entirely (the keep-forever default).
func (e *Engine) StartRunRetentionSweep(days int, interval time.Duration) {
	if days <= 0 {
		return
	}
	e.goBG(func() {
		select {
		case <-e.shutdown:
			return
		case <-time.After(retentionFirstSweepDelay):
		}
		for {
			purged, err := e.purgeExpiredRunsOnce(days)
			if err != nil {
				common.SLog().Warn("run retention sweep failed", "error", err)
			} else if purged > 0 {
				common.SLog().Info("run retention sweep purged expired runs",
					"purged", purged, "retention_days", days)
			}
			select {
			case <-e.shutdown:
				return
			case <-time.After(interval):
			}
		}
	})
}

// purgeExpiredRunsOnce deletes artifacts for, then purges, every run older
// than the retention window. Returns how many run rows were purged.
func (e *Engine) purgeExpiredRunsOnce(days int) (int64, error) {
	ids, err := e.store.ListRunIDsOlderThan(days)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if e.ArtifactStore != nil {
		for _, id := range ids {
			if delErr := e.ArtifactStore.DeleteRunArtifacts(id); delErr != nil {
				common.SLog().Warn("run retention: artifact delete failed (row purge proceeds)",
					common.RunAttr(id), "error", delErr)
			}
		}
	}
	// A run can age past the cutoff in the instant between the list and
	// this purge — its artifacts would then outlive it until manually
	// removed. The window is milliseconds against a day-granularity
	// cutoff; accepted rather than complicating the purge into per-ID
	// deletes.
	return e.store.PurgeRunsOlderThan(days)
}
