package engine

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

const (
	remotePaginationDefaultRetries = 2
	remotePaginationMaxPages       = 100000
)

// runSourceAPIRemotePages dispatches the safe subset of offset/numbered
// pagination whose termination is visible from the returned dataset (an
// empty page or max_records). Response metadata such as end_flag and
// total_pages_path remains on the existing in-process path until the worker
// result protocol carries page control metadata as well.
func (r *Runner) runSourceAPIRemotePages(node models.Node, source, sourceType string, paginationCfg, execCfg map[string]interface{}) (*common.DataSet, bool, error) {
	if r.instanceJobQueue == nil || r.dryRun || sourceType != "rest" {
		return nil, false, nil
	}
	if checkpointEvery, ok := execConfigInt(execCfg, "checkpoint_every"); ok && checkpointEvery > 0 {
		return nil, false, nil
	}
	if response, _ := node.Config["response"].(string); response == "artifact" {
		return nil, false, nil
	}
	if _, ok := execCfg["requests_per_second"]; ok {
		return nil, false, nil
	}

	strategy, _ := paginationCfg["strategy"].(string)
	if strategy != "offset" && strategy != "numbered" {
		return nil, false, nil
	}
	if strategy == "offset" {
		if intOptRemote(paginationCfg, "page_size", 0) <= 0 || stringOptRemote(paginationCfg, "end_flag", "") != "" {
			return nil, false, nil
		}
	}
	if strategy == "numbered" && stringOptRemote(paginationCfg, "total_pages_path", "") != "" {
		return nil, false, nil
	}

	maxConcurrency := intOptRemote(execCfg, "max_concurrency", 1)
	if maxConcurrency < 1 {
		maxConcurrency = 1
	}
	maxRecords := intOptRemote(paginationCfg, "max_records", 0)
	pageRetries := intOptRemote(execCfg, "page_max_retries", -1)
	if pageRetries < 0 {
		pageRetries = intOptRemote(node.Config, "max_retries", remotePaginationDefaultRetries)
	}
	if pageRetries < 0 {
		pageRetries = 0
	}
	backoff := stringOptRemote(execCfg, "page_retry_backoff", stringOptRemote(node.Config, "retry_backoff", "exponential"))
	timeoutSec := intOptRemote(node.Config, "timeout", 30)
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	allRows := make([]common.DataRow, 0)
	var columns []string
	pagesFetched := 0
	for pagesFetched < remotePaginationMaxPages {
		batch := minRemote(maxConcurrency, remotePaginationMaxPages-pagesFetched)
		specs := make([]remotePageSpec, batch)
		for i := range specs {
			pageNumber := pagesFetched + i
			specs[i] = buildRemotePageSpec(strategy, paginationCfg, pageNumber)
		}

		results := make([]remotePageResult, batch)
		var wg sync.WaitGroup
		for i := range specs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i] = r.dispatchSourceAPIPage(node, source, sourceType, specs[i], node.Config, timeoutSec, pageRetries, backoff)
			}(i)
		}
		wg.Wait()

		for i, result := range results {
			if result.err != nil {
				return nil, true, fmt.Errorf("pagination page %s: %w", specs[i].instanceKey, result.err)
			}
			if result.data == nil {
				return nil, true, fmt.Errorf("pagination page %s returned no dataset", specs[i].instanceKey)
			}
			if len(columns) == 0 {
				columns = append(columns, result.data.Columns...)
			}
			allRows = append(allRows, result.data.Rows...)
			pagesFetched++
			if maxRecords > 0 && len(allRows) >= maxRecords {
				allRows = allRows[:maxRecords]
				return &common.DataSet{Columns: columns, Rows: allRows}, true, nil
			}
			if len(result.data.Rows) == 0 {
				return &common.DataSet{Columns: columns, Rows: allRows}, true, nil
			}
		}
	}
	return nil, true, fmt.Errorf("pagination exceeded maximum of %d pages", remotePaginationMaxPages)
}

type remotePageSpec struct {
	instanceKey string
	params      map[string]string
}

type remotePageResult struct {
	data *common.DataSet
	err  error
}

func buildRemotePageSpec(strategy string, cfg map[string]interface{}, index int) remotePageSpec {
	if strategy == "offset" {
		pageSize := intOptRemote(cfg, "page_size", 1)
		offset := index * pageSize
		return remotePageSpec{
			instanceKey: fmt.Sprintf("page-%d", index),
			params: map[string]string{
				stringOptRemote(cfg, "offset_param", "offset"): strconvRemote(offset),
				stringOptRemote(cfg, "limit_param", "limit"):   strconvRemote(pageSize),
			},
		}
	}
	page := intOptRemote(cfg, "start", 1) + index
	return remotePageSpec{
		instanceKey: fmt.Sprintf("page-%d", page),
		params: map[string]string{
			stringOptRemote(cfg, "page_param", "page"): strconvRemote(page),
		},
	}
}

func (r *Runner) dispatchSourceAPIPage(node models.Node, source, sourceType string, spec remotePageSpec, config map[string]interface{}, timeoutSec, maxRetries int, backoff string) remotePageResult {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptStore, fencingGeneration, err := r.claimRemoteInstanceAttempt(node.ID, spec.instanceKey, attempt)
		if err != nil {
			return remotePageResult{err: err}
		}
		workOrder := &extensions.InstanceWorkOrder{
			NodeType:       string(models.NodeTypeSourceAPI),
			Config:         config,
			SourceURL:      source,
			SourceType:     sourceType,
			PageParams:     spec.params,
			TimeoutSeconds: timeoutSec,
		}
		data, dispatchErr := r.dispatchInstanceWorkOrderRemotely(node.ID, attempt, spec.instanceKey, fencingGeneration, workOrder, timeoutSec, nil)
		if dispatchErr == nil {
			return remotePageResult{data: data}
		}
		lastErr = dispatchErr
		if failErr := attemptStore.FailAttempt(r.run.ID, node.ID, spec.instanceKey, attempt, fencingGeneration, dispatchErr.Error()); failErr != nil {
			lastErr = fmt.Errorf("%v; settle failed: %w", dispatchErr, failErr)
		}
		if attempt < maxRetries {
			time.Sleep(remotePageRetryDelay(backoff, attempt))
		}
	}
	return remotePageResult{err: lastErr}
}

func (r *Runner) claimRemoteInstanceAttempt(nodeID, instanceKey string, attempt int) (store.ExecutionAttemptStore, int64, error) {
	attemptStore, ok := r.store.(store.ExecutionAttemptStore)
	if !ok {
		return nil, 0, fmt.Errorf("pagination instance dispatch: store does not support execution attempts")
	}
	idempotencyKey := fmt.Sprintf("%s:%s:%s:%d", r.run.ID, nodeID, instanceKey, attempt)
	if err := r.store.WithTx(func(tx *sql.Tx) error {
		return attemptStore.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID: r.run.ID, NodeID: nodeID, InstanceKey: instanceKey, Attempt: attempt,
			Status: models.AttemptStatusQueued, IdempotencyKey: idempotencyKey,
		})
	}); err != nil {
		return nil, 0, fmt.Errorf("persist pagination instance %s attempt %d: %w", instanceKey, attempt, err)
	}
	gen, claimed, err := attemptStore.ClaimAttempt(r.run.ID, nodeID, instanceKey, attempt, r.instanceID, store.DefaultLeaseDuration)
	if err != nil {
		return nil, 0, fmt.Errorf("claim pagination instance %s attempt %d: %w", instanceKey, attempt, err)
	}
	if !claimed {
		return nil, 0, fmt.Errorf("pagination instance %s attempt %d was already claimed", instanceKey, attempt)
	}
	if err := attemptStore.AckAttempt(r.run.ID, nodeID, instanceKey, attempt, r.instanceID, gen); err != nil {
		return nil, 0, fmt.Errorf("ack pagination instance %s attempt %d: %w", instanceKey, attempt, err)
	}
	return attemptStore, gen, nil
}

func remotePageRetryDelay(backoff string, attempt int) time.Duration {
	if strings.EqualFold(backoff, "linear") {
		return time.Duration(attempt+1) * time.Second
	}
	if strings.EqualFold(backoff, "none") {
		return 0
	}
	seconds := 1 << minRemote(attempt, 5)
	return time.Duration(seconds) * time.Second
}

func intOptRemote(cfg map[string]interface{}, key string, fallback int) int {
	switch value := cfg[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	}
	return fallback
}

func stringOptRemote(cfg map[string]interface{}, key, fallback string) string {
	if value, ok := cfg[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

func strconvRemote(value int) string {
	return fmt.Sprintf("%d", value)
}

func minRemote(a, b int) int {
	if a < b {
		return a
	}
	return b
}
