package fetchers

import "github.com/Tnsor-Labs/brokoli/pkg/common"

// PaginationCheckpoint captures enough state to resume a paginated fetch
// from where it left off, without re-fetching pages already retrieved. Only
// the field(s) relevant to the checkpoint's Strategy are populated.
type PaginationCheckpoint struct {
	Strategy     string `json:"strategy"`
	PagesFetched int    `json:"pages_fetched"`

	// Offset is the next offset to request — "offset" strategy.
	Offset int `json:"offset,omitempty"`
	// Page is the next page number to request — "numbered" strategy.
	Page int `json:"page,omitempty"`
	// Cursor is the cursor token for the next request — "cursor" strategy.
	Cursor string `json:"cursor,omitempty"`
	// NextURL is the complete next-page URL — "next_link" and "link_header"
	// strategies, both of which page via a server-supplied absolute URL
	// rather than a param the client computes.
	NextURL string `json:"next_url,omitempty"`
}

// CheckpointSaver is called after every checkpoint_every pages (per the
// SDK's execution() policy) with the checkpoint position and the records
// accumulated so far, so the caller can persist both durably. A save
// failure should be logged by the caller — it must never be treated as a
// fetch failure, mirroring this codebase's existing artifact-write-failure
// policy (see ArtifactStore.WriteArtifact's callers).
type CheckpointSaver func(checkpoint PaginationCheckpoint, recordsSoFar []map[string]interface{}) error

// CheckpointingFetcher is implemented by fetchers that support resuming a
// paginated fetch from a prior checkpoint. Fetchers that don't implement it
// (or aren't fetching a paginated source at all) are used via the plain
// Fetcher interface and always start from the beginning.
type CheckpointingFetcher interface {
	Fetcher

	// FetchPaginatedResumable is like Fetch, but for a paginated source_api
	// node: resume (nil for a fresh fetch) picks up from a prior checkpoint
	// instead of page one — resumeRecords is that checkpoint's previously
	// accumulated records, prepended to the result instead of re-fetched.
	// onCheckpoint (nil to disable checkpointing entirely) is called
	// periodically per the execution() policy's checkpoint_every. Calling
	// this on a non-paginated config (no "pagination" in options) is
	// equivalent to Fetch — resume/resumeRecords/onCheckpoint are unused.
	FetchPaginatedResumable(source string, options map[string]interface{}, resume *PaginationCheckpoint, resumeRecords []map[string]interface{}, onCheckpoint CheckpointSaver) (*common.DataSet, error)
}
