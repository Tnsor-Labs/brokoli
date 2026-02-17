package cmd

import (
	"database/sql"
	"fmt"
	"io/fs"
	"log"

	"github.com/hc12r/broked/api"
	"github.com/hc12r/broked/crypto"
	"github.com/hc12r/broked/engine"
	"github.com/hc12r/broked/store"
	"github.com/hc12r/broked/ui"
	"github.com/spf13/cobra"
)

var (
	port   int
	dbPath string
	apiKey string
)

var rootCmd = &cobra.Command{
	Use:   "broked",
	Short: "Broked — Data Orchestration Platform",
	Long:  "A data-aware orchestration engine with a minimalist UI. Built on top of BrokoliSQL.",
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Broked server",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store.NewStore(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer s.Close()
		log.Printf("Database: %s", store.Describe(dbPath))

		eng := engine.NewEngine(s)

		// Wire variable store after crypto is loaded (see below)

		sched := engine.NewScheduler(eng, s)
		if err := sched.Start(); err != nil {
			log.Printf("WARNING: scheduler failed to start: %v", err)
		}
		defer sched.Stop()

		var uiFS fs.FS
		if distFS, err := fs.Sub(ui.Dist, "dist"); err == nil {
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

		srv := api.NewServer(port, s, eng, uiFS, auth, userStore, sched, cryptoCfg)
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
	serveCmd.Flags().StringVar(&dbPath, "db", "./broked.db", "SQLite database path")
	serveCmd.Flags().StringVar(&apiKey, "api-key", "", "Enable auth with this API key")
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(generateKeyCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
