package engine

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
)

// sharedBlobStoreFromEnv builds the cross-pod blob store from
// configuration, or returns nil when none is configured.
//
// ADR-038: a distributed deployment needs somewhere both the pod that
// stages a task input and the pod that serves the fetch can reach.
// pkg/artifact.S3Store has been able to do this since #268 and gained
// digest resolution in #561, but nothing ever constructed it: NewS3Store
// had zero non-test callers. This is that caller.
//
// Nil is a legitimate result, not a failure. A single-node install has
// one process and needs none of this. What must not happen is silently
// falling back to per-pod disk, which is the defect this exists to fix,
// so callers that need a shared store refuse by name instead.
func sharedBlobStoreFromEnv() artifact.Store {
	bucket := os.Getenv("BROKOLI_BLOB_S3_BUCKET")
	if bucket == "" {
		return nil
	}

	cfg := artifact.S3StoreConfig{
		Bucket:          bucket,
		Region:          os.Getenv("BROKOLI_BLOB_S3_REGION"),
		Endpoint:        os.Getenv("BROKOLI_BLOB_S3_ENDPOINT"),
		AccessKeyID:     os.Getenv("BROKOLI_BLOB_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("BROKOLI_BLOB_S3_SECRET_ACCESS_KEY"),
	}
	if v, err := strconv.ParseBool(os.Getenv("BROKOLI_BLOB_S3_PATH_STYLE")); err == nil {
		cfg.UsePathStyle = v
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := artifact.NewS3Store(ctx, cfg)
	if err != nil {
		// Loud, and still nil. A misconfigured bucket must not read as
		// "no bucket configured": the first is an operator mistake worth
		// shouting about, the second is an ordinary single-node install.
		// Both end with no shared store, and the caller's refusal names
		// which one happened because this line is in the log above it.
		log.Printf("shared blob store: bucket %q is configured but unusable, "+
			"reference-based task input will be refused: %v", bucket, err)
		return nil
	}
	log.Printf("Shared blob store: s3 bucket %q, for cross-pod staged task inputs", bucket)
	return st
}
