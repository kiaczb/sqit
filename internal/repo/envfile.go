package repo

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvFileName is the per-project settings file that holds the connection
// secrets. It lives next to .sqit/ and is never committed.
const EnvFileName = "sqit.env"

// LoadEnvFile reads KEY=VALUE lines from path into the process environment.
// Variables that are already set win, so the real environment always
// overrides the file. A missing file is not an error.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if v == "" {
			continue // an empty value means "not configured here"
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return sc.Err()
}

// EnvFileTemplate renders the sqit.env written by `sqit init`. Known values
// are filled in; secrets are left for the user.
func EnvFileTemplate(cfg *Config) string {
	var b strings.Builder
	b.WriteString("# sqit connection settings for this project.\n")
	b.WriteString("# This file holds secrets: it is listed in .gitignore and must NOT be committed.\n")
	b.WriteString("# Values here are used only when the variable is not already set in the environment.\n\n")
	b.WriteString("# Passwords for the connections described in .sqit/config.json\n")
	b.WriteString(fmt.Sprintf("# (LOCAL: %s, UPSTREAM/TEST: %s)\n", cfg.Local.Describe(), cfg.Test.Describe()))
	fmt.Fprintf(&b, "%s=\n", orDefault(cfg.Local.PasswordEnv, "SQIT_LOCAL_PASSWORD"))
	fmt.Fprintf(&b, "%s=\n\n", orDefault(cfg.Test.PasswordEnv, "SQIT_TEST_PASSWORD"))
	b.WriteString("# Alternatively give complete DSNs (go-sql-driver format); they override host/port/user/database from config.json.\n")
	fmt.Fprintf(&b, "#%s=user:password@tcp(127.0.0.1:3306)/local_db\n", orDefault(cfg.Local.DSNEnv, "SQIT_LOCAL_DSN"))
	fmt.Fprintf(&b, "#%s=user:password@tcp(test-host:3306)/test_db\n\n", orDefault(cfg.Test.DSNEnv, "SQIT_TEST_DSN"))
	b.WriteString("# Refuse any upstream that is not on localhost (recommended while testing against a local MySQL).\n")
	if cfg.LocalOnly {
		b.WriteString("SQIT_LOCAL_ONLY=1\n")
	} else {
		b.WriteString("#SQIT_LOCAL_ONLY=1\n")
	}
	return b.String()
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// WriteEnvFile creates sqit.env in root if it does not exist yet and makes
// sure .gitignore excludes it. It reports what it did.
func WriteEnvFile(root string, cfg *Config) (created bool, gitignored bool, err error) {
	p := filepath.Join(root, EnvFileName)
	if _, err := os.Stat(p); err == nil {
		created = false
	} else {
		if err := os.WriteFile(p, []byte(EnvFileTemplate(cfg)), 0o600); err != nil {
			return false, false, err
		}
		created = true
	}
	gi := filepath.Join(root, ".gitignore")
	b, err := os.ReadFile(gi)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return created, false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(line)
		if l == EnvFileName || l == "/"+EnvFileName || l == "*.env" {
			return created, false, nil
		}
	}
	content := string(b)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "# sqit connection secrets\n" + EnvFileName + "\n"
	if err := os.WriteFile(gi, []byte(content), 0o644); err != nil {
		return created, false, err
	}
	return created, true, nil
}
