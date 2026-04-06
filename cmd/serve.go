package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Tnsor-Labs/brokoli/api"
	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
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
	Short: "Broked — Data Orchestration Platform",
	Long:  "A data-aware orchestration engine with a minimalist UI. Built on top of BrokoliSQL.",
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Broked server",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Initialize extensions (community defaults unless overridden by enterprise binary)
		if Extensions == nil {
			Extensions = extensions.DefaultRegistry()
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

		eng := engine.NewEngine(s)

		// Wire job queue from extensions (enterprise distributed mode)
		if Extensions != nil && Extensions.JobQueue != nil && RunMode != "all" {
			eng.JobQueue = Extensions.JobQueue
			log.Printf("Job queue enabled (mode: %s)", RunMode)
		}

		// Only start scheduler if mode is "all" or "scheduler"
		var sched *engine.Scheduler
		if RunMode == "all" || RunMode == "scheduler" {
			sched = engine.NewScheduler(eng, s)
			if err := sched.Start(); err != nil {
				log.Printf("WARNING: scheduler failed to start: %v", err)
			}
			defer sched.Stop()
		}

		// Platform services (enterprise: trial checker, SLA checker, etc)
		if Extensions != nil && Extensions.Platform != nil && Extensions.Platform.Enabled() {
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
		keyPath := dbPath + ".key"
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
		eng.ConnResolver = engine.NewConnectionResolver(s, cryptoCfg)
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
			for {
				job, err := Extensions.JobQueue.Dequeue()
				if err != nil {
					if err == extensions.ErrQueueClosed {
						return nil
					}
					log.Printf("Dequeue error: %v", err)
					time.Sleep(time.Second)
					continue
				}
				if job.PipelineID == "" {
					continue // empty job (timeout)
				}
				log.Printf("Worker: executing pipeline %s (run %s)", job.PipelineID, job.RunID)
				go func(j extensions.RunJob) {
					if _, err := eng.RunPipeline(j.PipelineID, j.Params); err != nil {
						log.Printf("Worker: run failed: %v", err)
						Extensions.JobQueue.Fail(j.ID, err)
					} else {
						Extensions.JobQueue.Ack(j.ID)
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
	serveCmd.Flags().StringVar(&dbPath, "db", "./brokoli.db", "SQLite database path")
	serveCmd.Flags().StringVar(&apiKey, "api-key", "", "Enable auth with this API key")
	serveCmd.Flags().StringVar(&RunMode, "mode", "all", "Run mode: all, api, scheduler, worker")
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(generateKeyCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
