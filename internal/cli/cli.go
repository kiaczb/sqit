// Package cli implements the sqit command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sqit/internal/core"
	"sqit/internal/repo"
)

// Command is one sub-command.
type Command struct {
	Name    string
	Summary string
	Run     func(ctx context.Context, env *Env, args []string) error
}

// Env carries the process environment for commands.
type Env struct {
	Dir    string // working directory
	Stdout io.Writer
	Stderr io.Writer
}

// ExitError carries a non-zero exit status without being a failure message.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

var commands = []*Command{
	{Name: "init", Summary: "create a .sqit repository", Run: runInit},
	{Name: "status", Summary: "show working tree and upstream state", Run: runStatus},
	{Name: "diff", Summary: "show changes (working tree vs HEAD; --upstream; --schema)", Run: runDiff},
	{Name: "commit", Summary: "record the LOCAL database state", Run: runCommit},
	{Name: "fetch", Summary: "snapshot the TEST database as upstream", Run: runFetch},
	{Name: "pull", Summary: "fetch and merge TEST into LOCAL (--dry-run)", Run: runPull},
	{Name: "table", Summary: "show a table's rows with their changes (--upstream, --json)", Run: runTable},
	{Name: "log", Summary: "show commit history", Run: runLog},
	{Name: "show", Summary: "show a commit and its changes", Run: runShow},
	{Name: "inspect", Summary: "print a raw repository object", Run: runInspect},
}

// Main runs the CLI and returns the process exit code.
func Main(args []string) int {
	env := &Env{Dir: ".", Stdout: os.Stdout, Stderr: os.Stderr}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(env.Stdout)
		return 0
	}
	name := args[0]
	for _, c := range commands {
		if c.Name == name {
			err := c.Run(context.Background(), env, args[1:])
			var ee ExitError
			if errors.As(err, &ee) {
				return ee.Code
			}
			if err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return 0
				}
				fmt.Fprintf(env.Stderr, "sqit %s: %v\n", name, err)
				return 1
			}
			return 0
		}
	}
	fmt.Fprintf(env.Stderr, "sqit: unknown command %q\n\n", name)
	usage(env.Stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "sqit – Git-like version control for MySQL data")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "usage: sqit <command> [options]")
	fmt.Fprintln(w)
	for _, c := range commands {
		fmt.Fprintf(w, "  %-9s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Passwords come from the environment variables named in .sqit/config.json")
	fmt.Fprintln(w, "(SQIT_LOCAL_PASSWORD / SQIT_TEST_PASSWORD, or full DSNs in SQIT_LOCAL_DSN / SQIT_TEST_DSN).")
}

func newFlags(name string, env *Env) *flag.FlagSet {
	fs := flag.NewFlagSet("sqit "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

func open(env *Env) (*core.Workspace, error) {
	w, err := core.Open(env.Dir)
	if err != nil {
		return nil, err
	}
	w.Warn = func(msg string) { fmt.Fprintln(env.Stderr, "warning:", msg) }
	return w, nil
}

// ---------------------------------------------------------------------------

func runInit(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("init", env)
	cfg := coreDefaultConfig()
	tables := fs.String("tables", "", "comma-separated list of tables to track (default: every table not listed in .sqitignore)")
	fs.StringVar(&cfg.Local.Host, "local-host", cfg.Local.Host, "LOCAL MySQL host")
	fs.IntVar(&cfg.Local.Port, "local-port", cfg.Local.Port, "LOCAL MySQL port")
	fs.StringVar(&cfg.Local.User, "local-user", cfg.Local.User, "LOCAL MySQL user")
	fs.StringVar(&cfg.Local.Database, "local-db", cfg.Local.Database, "LOCAL database name")
	fs.StringVar(&cfg.Test.Host, "test-host", cfg.Test.Host, "TEST MySQL host")
	fs.IntVar(&cfg.Test.Port, "test-port", cfg.Test.Port, "TEST MySQL port")
	fs.StringVar(&cfg.Test.User, "test-user", cfg.Test.User, "TEST MySQL user")
	fs.StringVar(&cfg.Test.Database, "test-db", cfg.Test.Database, "TEST database name")
	fs.BoolVar(&cfg.LocalOnly, "local-only", false, "refuse TEST/upstream connections that do not target localhost")
	noInput := fs.Bool("no-input", false, "never prompt; take everything from flags / sqit.env")
	interactive := fs.Bool("interactive", false, "prompt for connection details even when stdin is not a terminal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, t := range strings.Split(*tables, ",") {
		if t = strings.TrimSpace(t); t != "" {
			cfg.Tables = append(cfg.Tables, t)
		}
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// Refuse early instead of leaving a half-configured repository behind.
	if _, err := os.Stat(filepath.Join(env.Dir, repo.DirName, "config.json")); err == nil {
		return fmt.Errorf("repository already exists at %s", filepath.Join(env.Dir, repo.DirName))
	}

	// Ask for what the flags did not provide, when someone is there to answer.
	var localPw, testPw string
	dsnGiven := os.Getenv(cfg.Local.DSNEnv) != "" && os.Getenv(cfg.Test.DSNEnv) != ""
	if !*noInput && !dsnGiven && (cfg.Local.Database == "" || cfg.Test.Database == "") && (*interactive || stdinIsTerminal()) {
		localPw, testPw = interactiveInit(cfg, env.Stdout, set)
	}

	r, err := coreInit(env.Dir, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Initialized empty sqit repository in %s\n", r.Dir)
	created, ignored, err := repo.WriteEnvFile(r.Root, cfg)
	if err != nil {
		return err
	}
	envPath := filepath.Join(r.Root, repo.EnvFileName)
	if created && (localPw != "" || testPw != "") {
		if err := repo.SetEnvFileValues(envPath, map[string]string{cfg.Local.PasswordEnv: localPw, cfg.Test.PasswordEnv: testPw}); err != nil {
			return err
		}
	}
	if created {
		if localPw != "" && testPw != "" {
			fmt.Fprintf(env.Stdout, "Created %s with the passwords (git-ignored).\n", envPath)
		} else {
			fmt.Fprintf(env.Stdout, "Created %s – put the connection passwords (or DSNs) there.\n", envPath)
		}
	} else {
		fmt.Fprintf(env.Stdout, "Keeping existing %s.\n", envPath)
	}
	if ignored {
		fmt.Fprintf(env.Stdout, "Added %s to .gitignore.\n", repo.EnvFileName)
	}
	ignorePath := filepath.Join(r.Root, repo.IgnoreFileName)
	if _, err := os.Stat(ignorePath); err != nil {
		if err := os.WriteFile(ignorePath, []byte(repo.IgnoreTemplate), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Created %s – list tables that must not be versioned there.\n", ignorePath)
	}
	if len(cfg.Tables) == 0 {
		fmt.Fprintln(env.Stdout, "Tracking every table with a primary key (minus .sqitignore).")
	}
	fmt.Fprintf(env.Stdout, "LOCAL:    %s\n", cfg.Local.Describe())
	fmt.Fprintf(env.Stdout, "UPSTREAM: %s\n", cfg.Test.Describe())
	if cfg.IsLocalOnly() {
		fmt.Fprintln(env.Stdout, "Local-only mode: upstream connections are restricted to localhost.")
	}
	localSet := cfg.Local.Database != "" || os.Getenv(cfg.Local.DSNEnv) != ""
	testSet := cfg.Test.Database != "" || os.Getenv(cfg.Test.DSNEnv) != ""
	if !localSet || !testSet {
		fmt.Fprintf(env.Stdout, "Edit %s (or %s) to set the databases.\n", r.Dir+string(os.PathSeparator)+"config.json", repo.EnvFileName)
		return nil
	}
	// Try both connections so a typo shows up now, not at the first commit.
	if err := repo.LoadEnvFile(envPath); err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, tryConnect("LOCAL   ", cfg.Local))
	fmt.Fprintln(env.Stdout, tryConnect("UPSTREAM", cfg.Test))
	fmt.Fprintln(env.Stdout, "Next: sqit commit -m \"Initial state\"")
	return nil
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// parseArgs parses flags that may appear before or after positional
// arguments (Go's flag package stops at the first non-flag otherwise).
func parseArgs(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			// A non-boolean flag given as "-name value" takes the next arg.
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") {
				if f := fs.Lookup(name); f != nil {
					if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
						if i+1 < len(args) {
							i++
							flags = append(flags, args[i])
						}
					}
				}
			}
			continue
		}
		positional = append(positional, a)
	}
	return fs.Parse(append(flags, positional...))
}
