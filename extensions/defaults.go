package extensions

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// ── Default implementations (open source / community edition) ──

// CommunityAuth is the default (no external SSO).
type CommunityAuth struct{}

func (c *CommunityAuth) Name() string                                { return "builtin" }
func (c *CommunityAuth) Enabled() bool                               { return false }
func (c *CommunityAuth) Middleware() func(http.Handler) http.Handler { return nil }
func (c *CommunityAuth) CallbackHandler() http.HandlerFunc           { return nil }

// CommunityAudit is the default (no audit logging).
type CommunityAudit struct{}

func (c *CommunityAudit) Log(entry AuditEntry) error                     { return nil }
func (c *CommunityAudit) Query(filter AuditFilter) ([]AuditEntry, error) { return nil, nil }

// CommunityGitSync is the default (no git sync).
type CommunityGitSync struct{}

func (c *CommunityGitSync) Enabled() bool                    { return false }
func (c *CommunityGitSync) Push(pipelineID string) error     { return nil }
func (c *CommunityGitSync) Pull() (int, error)               { return 0, nil }
func (c *CommunityGitSync) WebhookHandler() http.HandlerFunc { return nil }
func (c *CommunityGitSync) Config() GitSyncConfig            { return GitSyncConfig{} }
func (c *CommunityGitSync) Status() GitSyncStatus            { return GitSyncStatus{} }

// CommunityNotifier is the default (no notifications).
type CommunityNotifier struct{}

func (c *CommunityNotifier) Name() string              { return "none" }
func (c *CommunityNotifier) Enabled() bool             { return false }
func (c *CommunityNotifier) Send(n Notification) error { return nil }

// CommunityLicense always returns community edition.
type CommunityLicense struct{}

func (c *CommunityLicense) Validate() (*LicenseInfo, error) {
	return &LicenseInfo{
		Edition:  "community",
		Features: []string{},
	}, nil
}
func (c *CommunityLicense) HasFeature(feature string) bool { return false }
func (c *CommunityLicense) Edition() string                { return "community" }

// CommunityContracts is the default (no contract validation).
type CommunityContracts struct{}

func (c *CommunityContracts) Validate(contract DataContract, columns []string, rows []map[string]interface{}) []ContractViolation {
	return nil
}

// CommunityPII is the default (no PII detection).
type CommunityPII struct{}

func (c *CommunityPII) Scan(columns []string, rows []map[string]interface{}, sampleSize int) []PIIDetection {
	return nil
}

// CommunityOpenLineage is the default (no OpenLineage emission).
type CommunityOpenLineage struct{}

func (c *CommunityOpenLineage) EmitRunStart(pipelineID, pipelineName, runID string) error {
	return nil
}
func (c *CommunityOpenLineage) EmitRunComplete(pipelineID, pipelineName, runID string, durationMs int64) error {
	return nil
}
func (c *CommunityOpenLineage) EmitRunFail(pipelineID, pipelineName, runID string, err string) error {
	return nil
}

// noopPlatform is the default (no platform features).
type noopPlatform struct{}

func (n *noopPlatform) Enabled() bool                                           { return false }
func (n *noopPlatform) RegisterRoutes(r, s, us interface{}, eng ...interface{}) {}
func (n *noopPlatform) StartServices(s interface{})                             {}
func (n *noopPlatform) StopServices()                                           {}
func (n *noopPlatform) MigrateDB(db interface{})                                {}

// noopTeam is the default (no team features).
type noopTeam struct{}

func (n *noopTeam) Enabled() bool                                      { return false }
func (n *noopTeam) RegisterRoutes(r, s interface{})                    {}
func (n *noopTeam) PermissionMiddleware(permission string) interface{} { return nil }
func (n *noopTeam) MigrateDB(db interface{})                           {}

// ── In-memory EventBus (single-process default) ──

// inMemoryEventBus implements EventBus using Go channels (single process).
type inMemoryEventBus struct {
	mu   sync.RWMutex
	subs map[string][]chan EventMessage
}

func newInMemoryEventBus() *inMemoryEventBus {
	return &inMemoryEventBus{subs: make(map[string][]chan EventMessage)}
}

func (b *inMemoryEventBus) Publish(channel string, data []byte) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	msg := EventMessage{Channel: channel, Data: data}
	for pattern, chs := range b.subs {
		if matchPattern(pattern, channel) {
			for _, ch := range chs {
				select {
				case ch <- msg:
				default: // drop if subscriber is slow
				}
			}
		}
	}
	return nil
}

func (b *inMemoryEventBus) Subscribe(pattern string) (<-chan EventMessage, func(), error) {
	ch := make(chan EventMessage, 64)
	b.mu.Lock()
	b.subs[pattern] = append(b.subs[pattern], ch)
	b.mu.Unlock()

	closer := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		subs := b.subs[pattern]
		for i, s := range subs {
			if s == ch {
				b.subs[pattern] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}
	return ch, closer, nil
}

func (b *inMemoryEventBus) Close() error { return nil }

// matchPattern checks if a channel matches a subscription pattern.
// Supports simple glob: "events:*" matches "events:run:123".
func matchPattern(pattern, channel string) bool {
	if pattern == channel || pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(channel, prefix)
	}
	return false
}

// ── In-memory JobQueue (single-process default) ──

// inMemoryJobQueue implements JobQueue using a Go channel (single process).
type inMemoryJobQueue struct {
	jobs   chan RunJob
	closed bool
	mu     sync.Mutex
}

func newInMemoryJobQueue() *inMemoryJobQueue {
	return &inMemoryJobQueue{jobs: make(chan RunJob, 1000)}
}

func (q *inMemoryJobQueue) Enqueue(job RunJob) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	q.mu.Unlock()

	select {
	case q.jobs <- job:
		return nil
	default:
		return fmt.Errorf("job queue full")
	}
}

func (q *inMemoryJobQueue) Dequeue() (RunJob, error) {
	job, ok := <-q.jobs
	if !ok {
		return RunJob{}, ErrQueueClosed
	}
	return job, nil
}

func (q *inMemoryJobQueue) Ack(jobID string) error             { return nil }
func (q *inMemoryJobQueue) Fail(jobID string, err error) error { return nil }

func (q *inMemoryJobQueue) Len() int {
	return len(q.jobs)
}

func (q *inMemoryJobQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.jobs)
	}
	return nil
}

// DefaultRegistry returns the community defaults.
func DefaultRegistry() *Registry {
	return &Registry{
		Auth:        &CommunityAuth{},
		Audit:       &CommunityAudit{},
		GitSync:     &CommunityGitSync{},
		License:     &CommunityLicense{},
		Notifier:    &CommunityNotifier{},
		Contracts:   &CommunityContracts{},
		PII:         &CommunityPII{},
		OpenLineage: &CommunityOpenLineage{},
		Platform:    &noopPlatform{},
		Team:        &noopTeam{},
		EventBus:    newInMemoryEventBus(),
		JobQueue:    newInMemoryJobQueue(),
	}
}
