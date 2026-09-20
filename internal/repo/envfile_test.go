package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvFileRoundTrip(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.Local.Database, cfg.Test.Database = "l", "t"
	cfg.LocalOnly = true

	created, ignored, err := WriteEnvFile(root, cfg)
	if err != nil || !created || !ignored {
		t.Fatalf("first write: created=%v ignored=%v err=%v", created, ignored, err)
	}
	// Second call keeps the file and does not duplicate the .gitignore line.
	created, ignored, err = WriteEnvFile(root, cfg)
	if err != nil || created || ignored {
		t.Fatalf("second write: created=%v ignored=%v err=%v", created, ignored, err)
	}
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if strings.Count(string(gi), EnvFileName) != 1 {
		t.Fatalf(".gitignore:\n%s", gi)
	}

	// Fill in values and load them; existing environment wins.
	env := filepath.Join(root, EnvFileName)
	content := "# comment\nSQIT_LOCAL_PASSWORD=secret\nexport SQIT_TEST_PASSWORD='quoted pw'\nSQIT_LOCAL_DSN=\nSQIT_LOCAL_ONLY=1\n"
	if err := os.WriteFile(env, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SQIT_LOCAL_PASSWORD", "from-env")
	os.Unsetenv("SQIT_TEST_PASSWORD")
	os.Unsetenv("SQIT_LOCAL_DSN")
	os.Unsetenv("SQIT_LOCAL_ONLY")
	if err := LoadEnvFile(env); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SQIT_LOCAL_PASSWORD") != "from-env" {
		t.Error("environment should override the file")
	}
	if os.Getenv("SQIT_TEST_PASSWORD") != "quoted pw" {
		t.Errorf("quoted value: %q", os.Getenv("SQIT_TEST_PASSWORD"))
	}
	if _, set := os.LookupEnv("SQIT_LOCAL_DSN"); set {
		t.Error("empty value must not be set")
	}
	if !cfg.IsLocalOnly() {
		t.Error("local only from file")
	}
	// Open loads the file automatically.
	os.Unsetenv("SQIT_TEST_PASSWORD")
	if _, err := Init(root, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SQIT_TEST_PASSWORD") != "quoted pw" {
		t.Error("Open should load sqit.env")
	}
	if err := LoadEnvFile(filepath.Join(root, "missing.env")); err != nil {
		t.Error("missing file must not be an error")
	}
}
