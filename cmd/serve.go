package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Tnsor-Labs/brokoli/api"
	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/tracing"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/Tnsor-Labs/brokoli/web"
	"github.com/spf13/cobra"
)

var (
	port   int
	dbPath string
	apiKey string
)

// RunMode controls which components this instance runs.
// "all" (default): API + Scheduler + Worker (single binary mode)
// "api": HTTP server + WebSocket only (enterprise distributed mode)
// "scheduler": Cron scheduler only (enterprise distributed mode)
// "worker": Pipeline executor only (enterprise distributed mode)
var RunMode = "all"

// Extensions is the plugin registry. Open source uses defaults.
// Enterprise binary overrides this before calling Execute().
var Extensions *extensions.Registry

// UIOverride allows the enterprise binary to provide its own UI assets.
// When set, this FS is used instead of the open source embedded UI.
var UIOverride fs.FS

var rootCmd = &cobra.Command{
	Use:   "brokoli",
	Short: "Brokoli — Data Orchestration Platform",
	Long:  "A data-aware orchestration engine with a minimalist UI. Built on top of BrokoliSQL.",
}

// SetVersion wires build-time version info (from main.go's ldflag vars)
// into the root command, which enables `brokoli --version` / `brokoli -v`.
// cobra auto-registers the flag when rootCmd.Version is non-empty.
//
// The custom template keeps the output parseable for scripts while
// still showing the commit/date a human would want at a glance:
//
//	brokoli v0.7.5 (abc123, 2026-04-13)
func SetVersion(version, commit, date string) {
	rootCmd.Version = version
	rootCmd.SetVersionTemplate(fmt.Sprintf(
		"brokoli %s (%s, %s)\n", version, shortCommit(commit), date,
	))
}

// shortCommit trims a git SHA to 7 characters unless it's a sentinel
// value like "none" (no commit info at build time).
func shortCommit(c string) string {
	if len(c) < 7 || c == "none" {
		return c
	}
	return c[:7]
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Brokoli server",
	RunE: func(cmd *cobra.Command, args []string) error {
		// OpenTelemetry tracing (Tnsor-Labs/brokoli#11): a no-op unless an
		// operator explicitly sets BROKOLI_OTEL_EXPORTER=otlp, so this is a
		// no-behavior-change default for every existing deployment — see
		// pkg/tracing's package doc comment.
		tracingShutdown, err := tracing.Init(cmd.Context())
		if err != nil {
			log.Printf("WARNING: tracing init failed, continuing without export: %v", err)
			tracingShutdown = func(context.Context) error { return nil }
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracingShutdown(shutdownCtx); err != nil {
				log.Printf("WARNING: tracing shutdown: %v", err)
			}
		}()

		syncPortEnvForSelfReferences(port)

		// Initialize extensions (community defaults unless overridden by enterprise binary)
		if Extensions == nil {
			Extensions = extensions.DefaultRegistry()
		}
		// Wire the plugin manager as a NodeExecutor. Plugins installed in
		// ~/.brokoli/plugins/ (or wherever BROKOLI_PLUGIN_DIR points) get
		// registered as node types that the engine's node-resolver loop
		// picks up alongside built-in types, without any special casing.
		// A missing plugin dir is not an error — fresh installs have zero
		// plugins until the user runs `brokoli plugins install`.
		if pluginMgr, err := plugins.NewManager(plugins.DefaultDir()); err != nil {
			log.Printf("plugins: %v (continuing without plugin support)", err)
		} else {
			// Always register the manager, even with zero plugins at boot:
			// a zero-type executor never matches CanHandle, and keeping the
			// same instance wired means a plugin installed later via the
			// API hot-reloads into the running engine without a restart.
			Extensions.Executors = append(Extensions.Executors, pluginMgr)
			if n := len(pluginMgr.NodeTypes()); n > 0 {
				log.Printf("plugins: %d node type(s) registered", n)
			}
		}
		license, _ := Extensions.License.Validate()
		log.Printf("Edition: %s", license.Edition)
		if license.Company != "" {
			log.Printf("Licensed to: %s", license.Company)
		}

		s, err := store.NewStore(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer s.Close()
		log.Printf("Database: %s", store.Describe(dbPath))
		warnIfSQLiteMultiInstanceRisk(dbPath, RunMode)

		eng := engine.NewEngine(s)
		// Registered after s.Close so it runs before it (defers are LIFO):
		// the engine's background goroutines — trigger-mode dependency
		// fan-out in particular — write through the store, so they must be
		// drained before the store closes underneath them. This is the
		// production half of Tnsor-Labs/brokoli#94; before it, shutdowns
		// logged "database is closed" from goroutines that outlived
		// everything.
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := eng.Close(ctx); err != nil {
				log.Printf("Engine shutdown: %v", err)
			}
		}()

		// Adapt resource defaults to the container this process actually
		// landed in (GOMEMLIMIT from the cgroup ceiling, run concurrency
		// from available memory) — see memlimit.go. Must run before the
		// scheduler starts or the worker loop sizes workerSlots off
		// GetQueueInfo, so the adapted concurrency is what everything
		// downstream observes.
		applyAdaptiveResourceDefaults(eng.SetMaxConcurrentRuns)

		// Recover runs a prior process left in a non-terminal status
		// (Tnsor-Labs/brokoli#9) — e.g. "running" because it was kill -9'd
		// mid-execution. Must happen before the scheduler starts firing new
		// runs and before any worker loop below begins pulling jobs, so
		// recovery never races a freshly dispatched run into being observed
		// mid-creation and misdiagnosed as orphaned.
		if _, err := eng.RecoverNonTerminalRuns(); err != nil {
			log.Printf("WARNING: startup recovery failed: %v", err)
		}

		// Scheduled run retention (Tnsor-Labs/brokoli#214): opt-in via
		// BROKOLI_RUN_RETENTION_DAYS; unset or 0 keeps runs forever,
		// exactly the old behavior. Safe on every instance concurrently —
		// the purge is one conditional bulk DELETE and artifact deletes
		// are idempotent.
		if v := os.Getenv("BROKOLI_RUN_RETENTION_DAYS"); v != "" {
			days, convErr := strconv.Atoi(v)
			switch {
			case convErr != nil || days < 0:
				log.Printf("WARNING: invalid BROKOLI_RUN_RETENTION_DAYS %q — retention disabled", v)
			case days > 0:
				eng.StartRunRetentionSweep(days, engine.DefaultRetentionSweepInterval)
				log.Printf("Run retention enabled: %d day(s), sweep every %s", days, engine.DefaultRetentionSweepInterval)
			}
		}

		// Wire the run-cancel broadcaster from extensions (enterprise
		// distributed mode). Every instance both broadcasts (CancelRun falls
		// back to it for runs it doesn't own) and subscribes (so a broadcast
		// reaches the instance that DOES own the run). Wired independently
		// of the job queue: an API pod in scheduler mode never dequeues jobs
		// but must still broadcast and receive cancels.
		if Extensions != nil && Extensions.CancelBroadcaster != nil {
			eng.CancelBroadcaster = Extensions.CancelBroadcaster
			if err := Extensions.CancelBroadcaster.SubscribeCancels(eng.CancelRelayedRun); err != nil {
				log.Printf("WARNING: run-cancel broadcaster subscription failed: %v (cross-instance cancels degraded)", err)
			} else {
				log.Printf("Run-cancel broadcaster enabled (mode: %s)", RunMode)
			}
		}

		// Wire job queue from extensions (enterprise distributed mode)
		if Extensions != nil && Extensions.JobQueue != nil && RunMode != "all" {
			eng.JobQueue = Extensions.JobQueue
			log.Printf("Job queue enabled (mode: %s)", RunMode)

			// Consume the dispatch outbox: re-enqueue accepted runs whose
			// queue delivery was lost (Tnsor-Labs/brokoli#216 — e.g. a
			// Redis restart wiping a non-persistent queue). Safe on every
			// instance concurrently: enqueue is idempotent by job ID and
			// the claim CAS settles genuine duplicates.
			eng.StartPendingRedispatchSweep(engine.DefaultPendingRedispatchInterval, engine.DefaultPendingRedispatchGrace)
			log.Printf("Pending-run redispatch sweep enabled (interval %s, grace %s)",
				engine.DefaultPendingRedispatchInterval, engine.DefaultPendingRedispatchGrace)

			// eng.ArtifactStore defaults to local disk (see
			// engine.NewEngine). In distributed mode that default is
			// wrong twice over. First, resume: a resumed run is claimed
			// by whichever worker dequeues it, but the original run's
			// artifacts sit on the ORIGINAL worker's own disk — restore
			// works only by placement luck. Second, lifecycle: the
			// TransientBlobJanitor reclaim (Tnsor-Labs/brokoli#215) only
			// engages on the SQL store, because only there are worker-
			// local blobs pure scratch — live verification of that fix
			// found it dormant on this exact deployment shape, worker
			// disks still growing, because this wiring used to happen
			// only alongside ADR-017 instance dispatch (which nothing
			// enables yet). Swap in the SQL-backed store — the same
			// database every pod already connects to — for EVERY
			// distributed deployment. Single-process "all" mode keeps
			// the local-disk default: no cross-pod problem to solve, no
			// reason to route artifacts through the database.
			if dialect, ok := sqlArtifactDialect(s); ok {
				if rawDB, ok := s.RawDB().(*sql.DB); ok {
					if artifactStore, err := engine.NewSQLArtifactStore(rawDB, dialect, os.Getenv("BROKOLI_ARTIFACT_DIR")); err != nil {
						log.Printf("WARNING: SQL artifact store init failed, cross-pod artifact reads degraded: %v", err)
					} else {
						eng.ArtifactStore = artifactStore
						log.Printf("Artifact store: SQL-backed (%s), for cross-pod artifacts and terminal blob reclaim", dialect)
					}
				}
			}

			// Wire the SAME queue for instance-level remote dispatch
			// (ADR-017). One shared queue rather than a second
			// transport: the worker loop below already dequeues both
			// whole-pipeline and WorkOrder-bearing jobs from this same
			// queue, branching on job.WorkOrder != nil.
			if instanceDispatchEnabled() {
				eng.InstanceJobQueue = Extensions.JobQueue
				log.Printf("Instance-level remote dispatch enabled (mode: %s)", RunMode)
			}
		}

		// Only start scheduler if mode is "all" or "scheduler"
		var sched *engine.Scheduler
		if RunMode == "all" || RunMode == "scheduler" {
			leaderForSched, cleanupLeader := newLeaderElector(s)
			defer cleanupLeader()
			sched = engine.NewScheduler(eng, s, leaderForSched)
			if err := sched.Start(); err != nil {
				log.Printf("WARNING: scheduler failed to start: %v", err)
			}
			defer sched.Stop()
		}

		// Platform services (enterprise: trial checker, SLA checker, etc)
		if Extensions != nil && Extensions.Platform != nil && Extensions.Platform.Enabled() && shouldStartPlatformServices(RunMode) {
			Extensions.Platform.StartServices(s)
			defer Extensions.Platform.StopServices()
		}

		var uiFS fs.FS
		if UIOverride != nil {
			uiFS = UIOverride
			log.Println("Serving enterprise UI")
		} else if distFS, err := fs.Sub(web.Dist, "dist"); err == nil {
			if _, err := fs.Stat(distFS, "index.html"); err == nil {
				uiFS = distFS
				log.Println("Serving embedded UI")
			}
		}

		// Setup auth
		auth := api.NewAuthConfig()
		if apiKey != "" {
			auth.AddKey(apiKey, "CLI-provided key")
			log.Println("API key authentication enabled")
		}

		// Setup user accounts
		var userStore *api.UserStore
		if rawDB, ok := s.RawDB().(*sql.DB); ok {
			us, err := api.NewUserStore(rawDB)
			if err != nil {
				log.Printf("WARNING: user store init failed: %v", err)
			} else {
				userStore = us
				// Account lockout lives in the store layer, which owns the
				// login_attempts schema and writes it correctly for both
				// dialects. UserStore used to reimplement it against a raw
				// *sql.DB and got the Postgres column types wrong, so no
				// attempt was ever recorded and no account was ever locked.
				if la, ok := s.(store.LoginAttemptStore); ok {
					userStore.UseLoginAttemptStore(la)
				} else {
					log.Printf("WARNING: this store does not implement LoginAttemptStore — account lockout after repeated failed logins is DISABLED")
				}
				switch count, cerr := userStore.UserCountErr(); {
				case cerr != nil:
					// Not fatal: the middleware refuses requests while the
					// count is unavailable, so the server is safe to start
					// and will recover when the database does.
					log.Printf("WARNING: cannot determine user count at startup (%v) — API requests will be refused until the database responds", cerr)
				case count == 0:
					log.Println("No users configured — running in open mode (create first user via API or UI)")
				default:
					log.Printf("User authentication enabled (%d users)", count)
				}
			}
		}

		// Encryption for connection secrets
		keyPath := encryptionKeyPath(dbPath)
		encKey, err := crypto.LoadOrCreateKey(keyPath)
		if err != nil {
			log.Printf("WARNING: could not load encryption key: %v", err)
			encKey = make([]byte, 32) // fallback zero key
		} else {
			log.Printf("Encryption key: %s", keyPath)
		}
		cryptoCfg := &crypto.Config{Key: encKey}

		// Wire variable store and connection resolver into engine
		eng.VarStore = engine.NewVarStoreAdapter(s, cryptoCfg)
		secretsChain := secrets.NewDefaultChain(cryptoCfg)
		eng.ConnResolver = engine.NewConnectionResolver(s, secretsChain)
		if Extensions != nil && len(Extensions.Executors) > 0 {
			eng.Executors = Extensions.Executors
			log.Printf("Enterprise: %d external executor(s) registered", len(Extensions.Executors))
		}
		// Wire notification provider from extensions (enterprise: Slack, PagerDuty, etc.)
		if Extensions != nil && Extensions.Notifier != nil {
			eng.Notifier = Extensions.Notifier
			if Extensions.Notifier.Enabled() {
				log.Printf("Notifications enabled (%s)", Extensions.Notifier.Name())
			}
		}

		// Worker-only mode: pull jobs from the queue and execute them
		if RunMode == "worker" {
			if Extensions == nil || Extensions.JobQueue == nil {
				return fmt.Errorf("worker mode requires a job queue (set BROKOLI_REDIS_URL)")
			}

			// Forward engine events to EventBus so API pods can broadcast via WebSocket
			if Extensions.EventBus != nil {
				go func() {
					for event := range eng.Events() {
						channel := "events:run"
						if event.OrgID != "" {
							channel = "events:org:" + event.OrgID
						}
						if data, err := json.Marshal(event); err == nil {
							Extensions.EventBus.Publish(channel, data)
						}
					}
				}()
				log.Println("Worker: forwarding events to EventBus")
			} else {
				// Drain the channel to prevent blocking
				go func() {
					for range eng.Events() {
					}
				}()
			}

			// Minimal HTTP server for Kubernetes liveness probing. Worker
			// mode has no leader concept (workers don't
			// run leader election — only scheduler/all do, see newLeaderElector
			// above), so sched is nil here and NewMinimalServer omits
			// /health/leader, registering only /health and /metrics. Runs in
			// the background: its own SIGINT/SIGTERM handling (api.Server.Start)
			// is independent of the dequeue loop's below, and either one
			// finishing is fine since main() exits once RunE returns.
			go func() {
				if err := api.NewMinimalServer(port, s, eng, nil).Start(); err != nil {
					log.Printf("Worker: health server error: %v", err)
				}
			}()

			log.Println("Worker mode: waiting for jobs...")
			_, workerCount := eng.GetQueueInfo()
			workerSlots := make(chan struct{}, workerCount)

			// Graceful shutdown: on SIGINT/SIGTERM,
			// stop claiming new jobs and give in-flight jobs a bounded window
			// to finish via drainWorkerSlots before returning, so
			// terminationGracePeriodSeconds in Kubernetes actually buys
			// something for worker pods. A job that doesn't finish in time is
			// abandoned to the execution-attempt lease/recovery system
			// (Tnsor-Labs/brokoli#6/#7/#9's RecoverNonTerminalRuns), same as
			// today's hard-kill case — this just makes the common case clean.
			quit := make(chan os.Signal, 1)
			signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

			shutdownDraining := func(sig os.Signal) {
				log.Printf("Worker: received %v, draining in-flight jobs (up to %s)...", sig, workerShutdownGracePeriod)
				if drainWorkerSlots(workerSlots, workerCount, workerShutdownGracePeriod) {
					log.Println("Worker: all in-flight jobs completed, shutting down cleanly")
				} else {
					log.Println("Worker: grace period expired with jobs still in-flight — abandoning to the recovery system")
				}
			}

			var lastAdmission time.Time
			for {
				// A second, independent admission gate alongside workerSlots'
				// count-based one (docs/adr/018-chunked-execution-and-backpressure.md):
				// don't claim a new slot at all while this pod is genuinely
				// low on memory, regardless of how much of workerCount is
				// still unused. Checked before entering the slot-claim select
				// below so a memory-constrained worker backs off instead of
				// racing into it. Also enforces memoryBackpressureSettleDelay
				// since the last successful admission — a cgroup reading
				// can't see a job that was just admitted but hasn't started
				// allocating yet, so a bare headroom check alone doesn't
				// prevent two jobs landing back-to-back before the first
				// one's memory footprint becomes visible.
				if admit, wait := admissionDecision(lastAdmission); !admit {
					select {
					case sig := <-quit:
						shutdownDraining(sig)
						return nil
					case <-time.After(wait):
					}
					continue
				}

				select {
				case sig := <-quit:
					shutdownDraining(sig)
					return nil
				case workerSlots <- struct{}{}:
					lastAdmission = time.Now()
				}

				type dequeueResult struct {
					job extensions.RunJob
					err error
				}
				resultCh := make(chan dequeueResult, 1)
				go func() {
					job, err := Extensions.JobQueue.Dequeue()
					resultCh <- dequeueResult{job, err}
				}()

				var job extensions.RunJob
				var err error
				select {
				case sig := <-quit:
					// A job may still arrive on resultCh later (buffered, so
					// that goroutine won't leak), but nobody will process it —
					// same abandon-to-recovery fallback as a job that times
					// out mid-execution below.
					<-workerSlots
					log.Printf("Worker: received %v while waiting for work, draining in-flight jobs (up to %s)...", sig, workerShutdownGracePeriod)
					if drainWorkerSlots(workerSlots, workerCount, workerShutdownGracePeriod) {
						log.Println("Worker: all in-flight jobs completed, shutting down cleanly")
					} else {
						log.Println("Worker: grace period expired with jobs still in-flight — abandoning to the recovery system")
					}
					return nil
				case res := <-resultCh:
					job, err = res.job, res.err
				}

				if err != nil {
					<-workerSlots
					if err == extensions.ErrQueueClosed {
						return nil
					}
					log.Printf("Dequeue error: %v", err)
					time.Sleep(time.Second)
					continue
				}
				if job.PipelineID == "" && job.WorkOrder == nil {
					<-workerSlots
					if job.ID != "" {
						if settleErr := Extensions.JobQueue.Ack(job.ID); settleErr != nil {
							log.Printf("Worker: reject invalid job %s: %v", job.ID, settleErr)
						}
					}
					continue // empty job (timeout) or invalid delivery
				}

				// ADR-017 instance dispatch: a WorkOrder-bearing job's
				// identity is (RunID, NodeID, InstanceKey, Attempt), not the
				// whole-pipeline job's own ID==RunID convention below —
				// job.ID is a fresh, independent identity for the job-queue
				// delivery itself, deliberately distinct from the attempt it
				// carries (see extensions.RunJob's own doc comment).
				if job.WorkOrder != nil {
					if job.ID == "" || job.RunID == "" || job.NodeID == "" || job.InstanceKey == "" {
						<-workerSlots
						log.Printf("Worker: rejecting invalid instance job identity: job=%q run=%q node=%q instance=%q", job.ID, job.RunID, job.NodeID, job.InstanceKey)
						if job.ID != "" {
							if settleErr := Extensions.JobQueue.Ack(job.ID); settleErr != nil {
								log.Printf("Worker: reject invalid instance job %s: %v", job.ID, settleErr)
							}
						}
						continue
					}
					log.Printf("Worker: executing instance %s of node %s (run %s)", job.InstanceKey, job.NodeID, job.RunID)
					go func(j extensions.RunJob) {
						defer func() { <-workerSlots }()
						if renewer, ok := Extensions.JobQueue.(extensions.JobQueueRenewer); ok {
							renewCtx, cancelRenew := context.WithCancel(context.Background())
							defer cancelRenew()
							go renewJobClaim(renewCtx, renewer, j.ID)
						}
						if execErr := engine.ExecuteInstanceJob(s, eng.ArtifactStore, j); execErr != nil {
							log.Printf("Worker: instance job failed: %v", execErr)
							if settleErr := Extensions.JobQueue.Fail(j.ID, execErr); settleErr != nil {
								log.Printf("Worker: fail instance job %s: %v", j.ID, settleErr)
							}
							return
						}
						if settleErr := Extensions.JobQueue.Ack(j.ID); settleErr != nil {
							log.Printf("Worker: ack instance job %s: %v", j.ID, settleErr)
						} else {
							log.Printf("Worker: completed instance %s of node %s (run %s)", j.InstanceKey, j.NodeID, j.RunID)
						}
					}(job)
					continue
				}

				if job.ID == "" || job.RunID == "" || job.ID != job.RunID {
					<-workerSlots
					log.Printf("Worker: rejecting invalid job identity: job=%q run=%q", job.ID, job.RunID)
					if job.ID != "" {
						if settleErr := Extensions.JobQueue.Ack(job.ID); settleErr != nil {
							log.Printf("Worker: reject invalid job %s: %v", job.ID, settleErr)
						}
					}
					continue
				}
				log.Printf("Worker: executing pipeline %s (run %s)", job.PipelineID, job.RunID)
				go func(j extensions.RunJob) {
					defer func() { <-workerSlots }()
					if renewer, ok := Extensions.JobQueue.(extensions.JobQueueRenewer); ok {
						renewCtx, cancelRenew := context.WithCancel(context.Background())
						defer cancelRenew()
						go renewJobClaim(renewCtx, renewer, j.ID)
					}
					run, err := eng.ExecuteQueuedRun(j.RunID, j.PipelineID, j.Params)
					if err != nil {
						log.Printf("Worker: run failed: %v", err)
						if run == nil {
							if errors.Is(err, engine.ErrInvalidQueuedRun) {
								if settleErr := Extensions.JobQueue.Ack(j.ID); settleErr != nil {
									log.Printf("Worker: discard invalid job %s: %v", j.ID, settleErr)
								}
								return
							}
							if settleErr := Extensions.JobQueue.Fail(j.ID, err); settleErr != nil {
								log.Printf("Worker: fail job %s: %v", j.ID, settleErr)
							}
							return
						}
					}
					if settleErr := Extensions.JobQueue.Ack(j.ID); settleErr != nil {
						log.Printf("Worker: ack job %s: %v", j.ID, settleErr)
					} else {
						log.Printf("Worker: completed run %s with status %s", j.RunID, run.Status)
					}
				}(job)
			}
		}

		// Scheduler-only mode: minimal HTTP server (health/metrics/leader),
		// no UI/auth/API routes. /health/leader
		// gives Kubernetes a real leader-aware readiness probe target instead
		// of falling back to bare process liveness. SIGINT/SIGTERM handling
		// (including the graceful cleanupLeader()/sched.Stop() shutdown
		// registered as defers above, which already correctly blocks on
		// releasing leadership before this function returns) is unchanged —
		// Start() traps the same signals api.NewServer's HTTP path always
		// has and returns once its own graceful HTTP shutdown completes.
		if RunMode == "scheduler" {
			log.Printf("Scheduler mode: starting HTTP server (health/metrics/leader) on port %d", port)
			return api.NewMinimalServer(port, s, eng, sched).Start()
		}

		// API or all mode: start HTTP server
		srv := api.NewServer(port, s, eng, uiFS, auth, userStore, sched, Extensions, cryptoCfg)
		return srv.Start()
	},
}

var generateKeyCmd = &cobra.Command{
	Use:   "generate-key",
	Short: "Generate a new API key",
	RunE: func(cmd *cobra.Command, args []string) error {
		key, err := api.GenerateKey()
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	},
}

func init() {
	serveCmd.Flags().IntVarP(&port, "port", "p", 8080, "HTTP server port")
	serveCmd.Flags().StringVar(&dbPath, "db", defaultDatabasePath(), "Database path or PostgreSQL URL")
	serveCmd.Flags().StringVar(&apiKey, "api-key", "", "Enable auth with this API key")
	serveCmd.Flags().StringVar(&RunMode, "mode", "all", "Run mode: all, api, scheduler, worker")
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(generateKeyCmd)
}

func defaultDatabasePath() string {
	if value := os.Getenv("BROKOLI_DB_URL"); value != "" {
		return value
	}
	return "./brokoli.db"
}

func encryptionKeyPath(databasePath string) string {
	if strings.Contains(databasePath, "://") {
		return "./brokoli.db.key"
	}
	return databasePath + ".key"
}

// syncPortEnvForSelfReferences keeps pkg/fetchers/rest_fetcher.go's
// self-reference URL resolution (used by source_api/sink_api nodes
// configured with a relative "/..." URL — e.g. the built-in example
// templates' source nodes, which fetch /api/samples/data/*.json) in sync
// with the port this server actually binds to. That resolution reads
// BROKOLI_SERVER_URL first, then falls back to "http://127.0.0.1:$PORT"
// (defaulting to 8080) — but --port/-p only ever set the local `port`
// variable, never the PORT env var, so a deployment started with any port
// other than 8080 had every self-referencing fetch silently resolve
// against the wrong port (or nothing at all) instead of this process.
// Only sets PORT when the operator hasn't already configured either —
// never overrides an explicit choice.
func syncPortEnvForSelfReferences(port int) {
	if os.Getenv("BROKOLI_SERVER_URL") == "" && os.Getenv("PORT") == "" {
		_ = os.Setenv("PORT", strconv.Itoa(port))
	}
}

func shouldStartPlatformServices(mode string) bool {
	return mode == "all" || mode == "scheduler"
}

// sqlArtifactDialect reports the SQL dialect engine.NewSQLArtifactStore
// needs for s, and whether s is a backend it supports at all — a type
// switch on the concrete store, not a URI re-parse, since s is already
// the constructed store.Store this process is using, not a connection
// string lying around to parse.
func sqlArtifactDialect(s store.Store) (string, bool) {
	switch s.(type) {
	case *store.PostgresStore:
		return "postgres", true
	case *store.SQLiteStore:
		return "sqlite", true
	default:
		return "", false
	}
}

// instanceDispatchEnabled reports whether ADR-017's instance-level remote
// dispatch should be wired onto the whole-pipeline JobQueue. Deliberately
// opt-in, not automatic just because a JobQueue is present: a distributed
// deployment already running today must not silently start dispatching
// dynamic-expansion instances remotely the moment this ships. When this
// returns false, eng.InstanceJobQueue stays nil and every expansion
// instance keeps executing in-process exactly as before.
func instanceDispatchEnabled() bool {
	return os.Getenv("BROKOLI_INSTANCE_DISPATCH") == "1"
}

// jobClaimRenewInterval is how often a job's transport-level claim is
// renewed (extensions.JobQueueRenewer) while it is still being processed.
// Found live: RunPipeline (a whole-pipeline job) and a single dynamic-
// expansion instance (a WorkOrder job) can both legitimately run past
// RedisJobQueue's own default 30s visibility timeout, at which point
// another idle worker's XAUTOCLAIM correctly-per-Redis-semantics but
// wrongly-for-us steals the still-in-flight job's claim — the true
// owner's own eventual Ack/Fail then fails with "not claimed". A fixed
// interval well under any reasonable visibility timeout, not derived
// from one: this queue has no way to know what timeout another
// JobQueueRenewer implementation might use.
const jobClaimRenewInterval = 10 * time.Second

// renewJobClaim periodically renews jobID's transport-level claim until
// ctx is cancelled — call cancel once the job's own processing returns,
// successfully or not, so this goroutine doesn't outlive it. A queue
// that doesn't implement extensions.JobQueueRenewer needs no renewal
// (nothing has a timeout to race), so callers should only start this
// after a successful type assertion.
func renewJobClaim(ctx context.Context, renewer extensions.JobQueueRenewer, jobID string) {
	ticker := time.NewTicker(jobClaimRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := renewer.RenewClaim(jobID); err != nil {
				log.Printf("Worker: renew claim for job %s: %v", jobID, err)
			}
		}
	}
}

// workerShutdownGracePeriod bounds how long --mode worker's dequeue loop
// waits for in-flight jobs to finish after receiving SIGINT/SIGTERM before
// giving up and returning anyway. Chosen to comfortably fit inside a
// Kubernetes pod's typical terminationGracePeriodSeconds (commonly 30s)
// with headroom for the SIGTERM-to-SIGKILL race: a job still running past
// this point is abandoned to the execution-attempt lease/recovery system
// (Tnsor-Labs/brokoli#6/#7/#9) rather than blocking shutdown indefinitely.
var workerShutdownGracePeriod = defaultWorkerDrainTimeout()

// defaultWorkerDrainTimeout resolves how long the dequeue loop waits for
// in-flight jobs on SIGTERM.
//
// 25 seconds fits inside Kubernetes' default 30s
// terminationGracePeriodSeconds, but it is far shorter than a real
// pipeline: measured on the lab cluster, a job of 56 seconds was
// abandoned by every graceful eviction — a KEDA scale-down, a node
// drain, an ordinary rolling deploy — and recovery then failed the run
// with "interrupted mid-execution" rather than retrying it. An
// autoscaling fleet therefore kills work every time it shrinks.
//
// BROKOLI_WORKER_DRAIN_TIMEOUT lets an operator match the window to
// their longest run. It only helps alongside a matching
// terminationGracePeriodSeconds — the kernel's SIGKILL is not
// negotiable — so the chart sets both from one value.
func defaultWorkerDrainTimeout() time.Duration {
	if v := os.Getenv("BROKOLI_WORKER_DRAIN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		log.Printf("WARNING: ignoring BROKOLI_WORKER_DRAIN_TIMEOUT=%q: expected a duration like 5m or a number of seconds", v)
	}
	return 25 * time.Second
}

// drainWorkerSlots waits for every in-flight worker job to finish, up to
// timeout, and reports whether it fully drained in time.
//
// It works by acquiring every slot in the workerSlots semaphore itself:
// each in-flight job holds exactly one slot for its duration (the dequeue
// loop above pushes a token before dispatching a job's goroutine; that
// goroutine pops it via `defer func() { <-workerSlots }()` when it
// finishes). Acquiring all `capacity` slots is therefore only possible once
// every currently in-flight job has released its own — the same trick a
// sync.WaitGroup.Wait() would give, reusing the semaphore that already
// exists rather than adding a second tracking mechanism.
//
// Extracted as a standalone function (rather than inlined in the dequeue
// loop) specifically so it's unit-testable without sending real OS signals
// to a running process.
func drainWorkerSlots(slots chan struct{}, capacity int, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		for i := 0; i < capacity; i++ {
			slots <- struct{}{}
		}
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// newLeaderElector builds the LeaderElector this instance's Scheduler
// should use (Tnsor-Labs/brokoli#10): a real Postgres-backed election for
// PostgresStore, or the always-leader NoopLeaderElector for anything else
// (SQLite — see warnIfSQLiteMultiInstanceRisk below for why SQLite never
// gets real coordination). The returned cleanup func must be deferred by
// the caller; it stops the background election loop and, for the Postgres
// case, releases held leadership so a replacement doesn't have to wait out
// the full lease duration on a clean shutdown.
func newLeaderElector(s store.Store) (leader store.LeaderElector, cleanup func()) {
	pg, ok := s.(*store.PostgresStore)
	if !ok {
		return store.NewNoopLeaderElector(), func() {}
	}
	db, ok := pg.RawDB().(*sql.DB)
	if !ok {
		log.Printf("WARNING: PostgresStore.RawDB() was not *sql.DB; leader election disabled, running as always-leader")
		return store.NewNoopLeaderElector(), func() {}
	}

	holderID := store.DefaultHolderID()
	elector := store.NewPostgresLeaderElector(db, holderID, store.DefaultLeaseDuration, store.DefaultRenewInterval)
	electCtx, cancel := context.WithCancel(context.Background())

	// Block on one synchronous election attempt, bounded so a degraded
	// Postgres can't stall this instance's entire startup — including its
	// HTTP server and read-only endpoints in "all" mode — before the
	// scheduler's Start() decides whether to run missed-run catch-up, so a
	// cold-booting instance never runs catch-up before it actually knows
	// its own leadership status (see engine.Scheduler.catchUpMissedRuns).
	// A timeout here just means this instance starts as a standby and
	// retries in the background on the next tick (at most
	// DefaultRenewInterval later); it is not a fatal error.
	acquireCtx, acquireCancel := context.WithTimeout(electCtx, 5*time.Second)
	if err := elector.Acquire(acquireCtx); err != nil {
		log.Printf("WARNING: initial leader election attempt failed (holder=%s), starting as standby — will keep retrying in the background: %v", holderID, err)
	}
	acquireCancel()

	// runDone lets the returned cleanup func actually wait for Run's
	// shutdown-release path to finish, rather than merely cancelling the
	// context and returning immediately. cancel() alone does not block on
	// the background goroutine's release() UPDATE completing, so without
	// this a graceful shutdown could still race the process exiting before
	// the lease was ever cleared — silently defeating the "a standby
	// doesn't have to wait out the full lease on a clean shutdown"
	// guarantee documented on PostgresLeaderElector.Run.
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		elector.Run(electCtx)
	}()
	log.Printf("Leader election enabled (Postgres, holder=%s, leader=%v)", holderID, elector.IsLeader())

	return elector, func() {
		cancel()
		<-runDone
	}
}

// warnIfSQLiteMultiInstanceRisk logs a loud warning when this instance is
// about to dispatch scheduled work against a SQLite backend in a run mode
// meant for multi-replica deployments. SQLite has no leader election
// (Tnsor-Labs/brokoli#10 implements real coordination for Postgres only —
// see store/postgres_leader.go's design note) and no distributed locking of
// any kind: running more than one "scheduler"/"all"-mode replica against
// the same SQLite file means every replica independently believes it's the
// only scheduler and will duplicate-dispatch every cron tick and catch-up
// pass, on top of SQLite's well-known intolerance of concurrent writers
// from separate processes (especially over a network filesystem).
//
// This binary has no way to know how many *other* replicas are configured
// — there is no --replica-count flag or equivalent anywhere in this repo
// today — so the practical version of guarding against this is a loud,
// hard-to-miss log line plus documentation, not a hard runtime reject that
// would need information we don't have. True enforcement belongs at the
// deployment/orchestration layer — out of scope here.
func warnIfSQLiteMultiInstanceRisk(dbURI, mode string) {
	if store.DriverName(dbURI) != "sqlite" {
		return
	}
	if mode != "all" && mode != "scheduler" {
		return
	}
	log.Printf("WARNING: SQLite backend has no multi-instance leader election or locking. "+
		"Running more than one replica of `brokoli serve --mode %s` against this same database file "+
		"WILL duplicate-dispatch scheduled pipeline runs and can corrupt the database under concurrent writers. "+
		"SQLite deployments must run exactly one instance — use a postgres:// database URL for multi-instance/HA deployments.", mode)
}

func Execute() error {
	return rootCmd.Execute()
}
