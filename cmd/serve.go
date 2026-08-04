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
		} else if len(pluginMgr.NodeTypes()) > 0 {
			Extensions.Executors = append(Extensions.Executors, pluginMgr)
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

		// Recover runs a prior process left in a non-terminal status
		// (Tnsor-Labs/brokoli#9) — e.g. "running" because it was kill -9'd
		// mid-execution. Must happen before the scheduler starts firing new
		// runs and before any worker loop below begins pulling jobs, so
		// recovery never races a freshly dispatched run into being observed
		// mid-creation and misdiagnosed as orphaned.
		if _, err := eng.RecoverNonTerminalRuns(); err != nil {
			log.Printf("WARNING: startup recovery failed: %v", err)
		}

		// Wire job queue from extensions (enterprise distributed mode)
		if Extensions != nil && Extensions.JobQueue != nil && RunMode != "all" {
			eng.JobQueue = Extensions.JobQueue
			log.Printf("Job queue enabled (mode: %s)", RunMode)
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
				if userStore.UserCount() == 0 {
					log.Println("No users configured — running in open mode (create first user via API or UI)")
				} else {
					log.Printf("User authentication enabled (%d users)", userStore.UserCount())
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

			log.Println("Worker mode: waiting for jobs...")
			_, workerCount := eng.GetQueueInfo()
			workerSlots := make(chan struct{}, workerCount)
			for {
				workerSlots <- struct{}{}
				job, err := Extensions.JobQueue.Dequeue()
				if err != nil {
					<-workerSlots
					if err == extensions.ErrQueueClosed {
						return nil
					}
					log.Printf("Dequeue error: %v", err)
					time.Sleep(time.Second)
					continue
				}
				if job.PipelineID == "" {
					<-workerSlots
					if job.ID != "" {
						if settleErr := Extensions.JobQueue.Ack(job.ID); settleErr != nil {
							log.Printf("Worker: reject invalid job %s: %v", job.ID, settleErr)
						}
					}
					continue // empty job (timeout) or invalid delivery
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

		// Scheduler-only mode: no HTTP server, just block until signal
		if RunMode == "scheduler" {
			log.Println("Scheduler mode: running (no HTTP server)")
			quit := make(chan os.Signal, 1)
			signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
			<-quit
			log.Println("Scheduler shutting down...")
			return nil
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

func shouldStartPlatformServices(mode string) bool {
	return mode == "all" || mode == "scheduler"
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
// deployment/orchestration layer (see brokoli-ee#9, HA deployment topology
// — out of scope here).
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
