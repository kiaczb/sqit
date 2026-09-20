package repo

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Connection references a MySQL database without holding its secret. The
// password is read from the environment variable named by PasswordEnv; a
// complete DSN in the variable named by DSNEnv overrides everything.
type Connection struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Database    string `json:"database"`
	PasswordEnv string `json:"passwordEnv"`
	DSNEnv      string `json:"dsnEnv"`
}

// Config is the content of .sqit/config.json.
type Config struct {
	Version int        `json:"version"`
	Local   Connection `json:"local"`
	Test    Connection `json:"test"`
	Tables  []string   `json:"tables"`
	// LocalOnly refuses any TEST/upstream connection that does not target
	// localhost. Meant for local development against a Docker MySQL so a
	// stray DSN can never reach the real TEST database. The environment
	// variable SQIT_LOCAL_ONLY=1 has the same effect.
	LocalOnly bool `json:"localOnly,omitempty"`
}

// LocalOnlyEnv is the environment variable that enables local-only mode.
const LocalOnlyEnv = "SQIT_LOCAL_ONLY"

// IsLocalOnly reports whether upstream connections are restricted to
// localhost, by config or by environment.
func (c *Config) IsLocalOnly() bool {
	if c.LocalOnly {
		return true
	}
	switch strings.ToLower(os.Getenv(LocalOnlyEnv)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// DefaultConfig returns a config template.
func DefaultConfig() *Config {
	return &Config{
		Version: 1,
		Local: Connection{Host: "127.0.0.1", Port: 3306, User: "root", Database: "",
			PasswordEnv: "SQIT_LOCAL_PASSWORD", DSNEnv: "SQIT_LOCAL_DSN"},
		Test: Connection{Host: "127.0.0.1", Port: 3306, User: "root", Database: "",
			PasswordEnv: "SQIT_TEST_PASSWORD", DSNEnv: "SQIT_TEST_DSN"},
		Tables: []string{},
	}
}

// DSN builds the go-sql-driver DSN for the connection. Session parameters
// that sqit relies on (utf8mb4, UTC, found-rows semantics) are always
// appended.
func (c Connection) DSN() (string, error) {
	var base string
	if c.DSNEnv != "" {
		if v := os.Getenv(c.DSNEnv); v != "" {
			base = v
		}
	}
	if base == "" {
		if c.Database == "" {
			return "", fmt.Errorf("connection has no database configured (and %s is not set)", orNone(c.DSNEnv))
		}
		pw := ""
		if c.PasswordEnv != "" {
			pw = os.Getenv(c.PasswordEnv)
		}
		host := c.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := c.Port
		if port == 0 {
			port = 3306
		}
		user := c.User
		if user == "" {
			user = "root"
		}
		cred := url.PathEscape(user)
		if pw != "" {
			cred += ":" + pw
		}
		base = fmt.Sprintf("%s@tcp(%s:%d)/%s", cred, host, port, c.Database)
	}
	// Append required parameters.
	params := []string{
		"charset=utf8mb4",
		"clientFoundRows=true",
		"parseTime=false",
		"interpolateParams=false",
		"multiStatements=false",
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + strings.Join(params, "&"), nil
}

// Describe renders the connection for humans (no secrets).
func (c Connection) Describe() string {
	if c.DSNEnv != "" && os.Getenv(c.DSNEnv) != "" {
		return "$" + c.DSNEnv
	}
	return fmt.Sprintf("%s@%s:%d/%s", c.User, c.Host, c.Port, c.Database)
}

func orNone(s string) string {
	if s == "" {
		return "<no dsnEnv>"
	}
	return "$" + s
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if c.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported config version %d", path, c.Version)
	}
	return &c, nil
}

func saveConfig(path string, c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// Save writes the config back to disk.
func (r *Repository) SaveConfig() error {
	return saveConfig(filepath.Join(r.Dir, "config.json"), r.Config)
}
