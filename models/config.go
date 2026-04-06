package models

// ServerConfig holds configuration for the Broked server.
type ServerConfig struct {
	Port   int    `json:"port"`
	DBPath string `json:"db_path"`
}

// DefaultServerConfig returns sensible defaults.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Port:   8080,
		DBPath: "./brokoli.db",
	}
}
