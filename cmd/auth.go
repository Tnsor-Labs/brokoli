package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// configDir returns ~/.brokoli, creating it if it doesn't exist.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".brokoli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// cliConfig is the persistent CLI config stored in ~/.brokoli/config.json.
type cliConfig struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func loadConfig() (*cliConfig, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return &cliConfig{}, nil
	}
	var cfg cliConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &cliConfig{}, nil
	}
	return &cfg, nil
}

func saveConfig(cfg *cliConfig) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600)
}

// resolveAuth reads the server URL and token from flags, falling back to
// ~/.brokoli/config.json when flags aren't explicitly set.
func resolveAuth(cmd *cobra.Command) (server, token string) {
	server, _ = cmd.Flags().GetString("server")
	token, _ = cmd.Flags().GetString("api-key")

	if cfg, err := loadConfig(); err == nil {
		if server == "http://localhost:8080" || server == "" {
			if cfg.Server != "" {
				server = cfg.Server
			}
		}
		if token == "" && cfg.Token != "" {
			token = cfg.Token
		}
	}
	return
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with a Brokoli server and store credentials",
	Long: `Prompts for server URL, username, and password, then stores the session
token in ~/.brokoli/config.json. Subsequent commands (run, import, export)
will use these credentials automatically — no need to pass --api-key.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		reader := bufio.NewReader(os.Stdin)
		serverFlag, _ := cmd.Flags().GetString("server")

		server := serverFlag
		if server == "" || server == "http://localhost:8080" {
			fmt.Print("Server URL [http://localhost:8080]: ")
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(line)
			if line != "" {
				server = line
			} else {
				server = "http://localhost:8080"
			}
		}
		server = strings.TrimRight(server, "/")

		fmt.Print("Username: ")
		username, _ := reader.ReadString('\n')
		username = strings.TrimSpace(username)
		if username == "" {
			return fmt.Errorf("username is required")
		}

		fmt.Print("Password: ")
		passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		password := string(passwordBytes)
		if password == "" {
			return fmt.Errorf("password is required")
		}

		// Authenticate via the API.
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
		resp, err := http.Post(server+"/api/auth/login", "application/json", strings.NewReader(body))
		if err != nil {
			return fmt.Errorf("connect to %s: %w", server, err)
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			var errResp map[string]string
			if json.Unmarshal(respBody, &errResp) == nil && errResp["error"] != "" {
				return fmt.Errorf("login failed: %s", errResp["error"])
			}
			return fmt.Errorf("login failed: HTTP %d", resp.StatusCode)
		}

		var tokenResp struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(respBody, &tokenResp); err != nil || tokenResp.Token == "" {
			return fmt.Errorf("unexpected response from server")
		}

		if err := saveConfig(&cliConfig{Server: server, Token: tokenResp.Token}); err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		fmt.Printf("Logged in to %s as %s\n", server, username)
		fmt.Println("Credentials saved to ~/.brokoli/config.json")
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Clear stored credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := saveConfig(&cliConfig{}); err != nil {
			return fmt.Errorf("clear config: %w", err)
		}
		fmt.Println("Logged out. Credentials cleared.")
		return nil
	},
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show current authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil || cfg.Server == "" || cfg.Token == "" {
			fmt.Println("Not logged in. Run: brokoli login")
			return nil
		}

		req, _ := http.NewRequest("GET", cfg.Server+"/api/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("connect to %s: %w", cfg.Server, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			fmt.Printf("Session expired on %s. Run: brokoli login\n", cfg.Server)
			return nil
		}

		var me map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&me)
		username, _ := me["username"].(string)
		role, _ := me["role"].(string)
		if username == "" {
			username = "anonymous"
		}
		fmt.Printf("Logged in to %s as %s", cfg.Server, username)
		if role != "" {
			fmt.Printf(" (%s)", role)
		}
		fmt.Println()
		return nil
	},
}

func init() {
	loginCmd.Flags().String("server", "", "Brokoli server URL")
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(whoamiCmd)
}
