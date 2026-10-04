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

func (r *Runner) runMigrate(node models.Node, attempt int) (*common.DataSet, error) {
	raw, partitioned := node.Config["partition"]
	if !partitioned || raw == nil {
		return r.runMigrateSingle(node, attempt)
	}
	partition, ranges, err := parseMigratePartitionConfig(raw)
	if err != nil {
		return nil, err
	}
	mode, _ := node.Config["mode"].(string)
	if !strings.EqualFold(mode, ModeUpsert) {
		return nil, fmt.Errorf("partitioned migrate requires mode=upsert so a retried partition cannot duplicate rows")
	}
	if len(configStringSlice(node.Config["key_columns"])) == 0 {
		return nil, fmt.Errorf("partitioned migrate requires key_columns for idempotent upsert")
	}
	if !containsStringFold(configStringSlice(node.Config["key_columns"]), partition.Column) {
		return nil, fmt.Errorf("partitioned migrate requires partition.column %q to be included in key_columns", partition.Column)
	}
	if create, _ := node.Config["create_table"].(bool); create {
		return nil, fmt.Errorf("partitioned migrate requires an existing destination table; create_table must be false")
	}
	sourceQuery, _ := node.Config["source_query"].(string)
	sourceURI, _ := node.Config["source_uri"].(string)
	if sourceQuery == "" || sourceURI == "" {
		return nil, fmt.Errorf("partitioned migrate requires a resolved source URI and source_query")
	}
	dialect := dialectForURI(sourceURI)
	if dialect == "" {
		dialect = "generic"
	}
	if r.instanceJobQueue != nil {
		if _, ok := r.store.(store.ExecutionAttemptStore); !ok {
			return nil, fmt.Errorf("partitioned migrate remote dispatch requires execution-attempt storage")
		}
		if stringValue(node.Config["source_conn_id"]) == "" || stringValue(node.Config["dest_conn_id"]) == "" {
			return nil, fmt.Errorf("partitioned migrate remote dispatch requires source_conn_id and dest_conn_id; direct URIs are not sent to workers")
		}
	}

	results := make([]*common.DataSet, len(ranges))
	errs := make(chan error, len(ranges))
	sem := make(chan struct{}, partition.MaxParallel)
	var wg sync.WaitGroup
	for i := range ranges {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-r.ctx.Done():
				errs <- r.ctx.Err()
				return
			}
			defer func() { <-sem }()
			result, err := r.runMigratePartition(node, attempt, partition, ranges[i], sourceQuery, dialect)
			if err != nil {
				errs <- fmt.Errorf("partition %d: %w", i, err)
				return
			}
			results[i] = result
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return nil, err
		}
	}

	total := 0
	for _, result := range results {
		if result == nil || len(result.Rows) == 0 {
			continue
		}
		if rows, ok := result.Rows[0]["migrated_rows"].(int); ok {
			total += rows
		} else if rows, ok := result.Rows[0]["migrated_rows"].(int64); ok {
			total += int(rows)
		} else if rows, ok := result.Rows[0]["migrated_rows"].(float64); ok {
			total += int(rows)
		}
	}
	destTable, _ := node.Config["dest_table"].(string)
	r.log(node.ID, models.LogLevelInfo, "Partitioned migration complete: %d rows migrated to %s in %d partitions", total, destTable, len(ranges))
	return migrateSummary(total, destTable, len(ranges)), nil
}

func (r *Runner) runMigratePartition(node models.Node, attempt int, partition migratePartitionConfig, bounds migratePartitionRange, sourceQuery, dialect string) (*common.DataSet, error) {
	key := fmt.Sprintf("range:%d", bounds.Index)
	if result, ok := r.reusedExpansionInstance(node.ID, key); ok {
		return result, nil
	}
	partitionQuery, args, err := migratePartitionQuery(sourceQuery, dialect, partition, bounds)
	if err != nil {
		return nil, err
	}
	partitionNode := node
	partitionNode.Config = cloneConfig(node.Config)
	partitionNode.Config["source_query"] = partitionQuery
	partitionNode.Config["_partition_args"] = args
	partitionNode.Config["partition"] = nil
	partitionNode.Config["create_table"] = false
	partitionNode.Config["_partition_disjoint"] = true

	instStore, _ := r.store.(store.ExpansionInstanceStore)
	started := time.Now().UTC()
	instance := &models.ExpansionInstance{
		ID: common.NewID(), RunID: r.run.ID, NodeID: node.ID, NodeAttempt: attempt,
		InstanceIndex: bounds.Index, InstanceKey: key, Status: models.RunStatusRunning, StartedAt: &started,
	}
	if instStore != nil && !r.dryRun {
		if err := instStore.CreateExpansionInstance(instance); err != nil {
			return nil, fmt.Errorf("persist partition: %w", err)
		}
	}

	var attempts store.ExecutionAttemptStore
	var fencing int64
	if as, ok := r.store.(store.ExecutionAttemptStore); ok && !r.dryRun {
		attempts = as
		if err := r.store.WithTx(func(tx *sql.Tx) error {
			return attempts.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
				RunID: r.run.ID, NodeID: node.ID, InstanceKey: key, Attempt: attempt,
				Status: models.AttemptStatusQueued, IdempotencyKey: fmt.Sprintf("%s:%s:%s:%d", r.run.ID, node.ID, key, attempt),
			})
		}); err != nil {
			return nil, fmt.Errorf("persist partition attempt: %w", err)
		}
		var claimed bool
		fencing, claimed, err = attempts.ClaimAttempt(r.run.ID, node.ID, key, attempt, r.instanceID, store.DefaultLeaseDuration)
		if err != nil || !claimed {
			if err == nil {
				err = fmt.Errorf("partition attempt already claimed or terminal")
			}
			return nil, err
		}
		if err := attempts.AckAttempt(r.run.ID, node.ID, key, attempt, r.instanceID, fencing); err != nil {
			return nil, err
		}
	}

	var result *common.DataSet
	if attempts != nil && r.instanceJobQueue != nil {
		work := &extensions.InstanceWorkOrder{
			NodeType: string(models.NodeTypeMigrate), RunID: r.run.ID, WorkspaceID: r.workspaceID(),
			Migrate: &extensions.MigrateWorkOrder{
				SourceConnID: stringValue(node.Config["source_conn_id"]), DestConnID: stringValue(node.Config["dest_conn_id"]),
				SourceQuery: sourceQuery, DestTable: stringValue(node.Config["dest_table"]),
				Dialect: dialect, Mode: stringValue(node.Config["mode"]), KeyColumns: configStringSlice(node.Config["key_columns"]),
				ChunkSize: intConfig(node.Config["chunk_size"]), Partition: node.Config["partition"].(map[string]interface{}), PartitionIndex: bounds.Index,
			}, TimeoutSeconds: migrateTimeout(node),
		}
		if stringValue(node.Config["source_uri"]) != "" || stringValue(node.Config["dest_uri"]) != "" {
			return nil, fmt.Errorf("partitioned migrate remote work order cannot contain direct connection URIs")
		}
		result, err = r.dispatchInstanceWorkOrderRemotely(node.ID, attempt, key, fencing, work, migrateTimeout(node), nil)
	} else {
		result, err = r.runMigrateSingle(partitionNode, attempt)
	}
	if err != nil {
		if instStore != nil && !r.dryRun {
			instance.Status = models.RunStatusFailed
			instance.DurationMs = time.Since(started).Milliseconds()
			instance.Error = err.Error()
			_ = instStore.UpdateExpansionInstance(instance)
		}
		if attempts != nil {
			_ = attempts.FailAttempt(r.run.ID, node.ID, key, attempt, fencing, err.Error())
		}
		return nil, err
	}
	rowCount := migrateResultRows(result)
	if instStore != nil && !r.dryRun {
		instance.Status = models.RunStatusSuccess
		instance.DurationMs = time.Since(started).Milliseconds()
		instance.RowCount = rowCount
		_ = instStore.UpdateExpansionInstance(instance)
	}
	if attempts != nil {
		_ = attempts.CompleteAttempt(r.run.ID, node.ID, key, attempt, fencing)
	}
	r.rememberExpansionInstance(node.ID, key, result)
	return result, nil
}

func migrateResultRows(result *common.DataSet) int {
	if result == nil || len(result.Rows) == 0 {
		return 0
	}
	switch value := result.Rows[0]["migrated_rows"].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func migrateTimeout(node models.Node) int {
	if value := intConfig(node.Config["timeout"]); value > 0 {
		return value
	}
	return 30 * 60
}

func intConfig(raw interface{}) int {
	value, ok := migratePartitionInt(raw)
	if !ok {
		return 0
	}
	return value
}

func stringValue(raw interface{}) string {
	value, _ := raw.(string)
	return value
}

func boolConfig(raw interface{}) bool {
	value, _ := raw.(bool)
	return value
}

func containsStringFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func cloneConfig(input map[string]interface{}) map[string]interface{} {
	output := make(map[string]interface{}, len(input)+1)
	for key, value := range input {
		output[key] = value
	}
	return output
}
