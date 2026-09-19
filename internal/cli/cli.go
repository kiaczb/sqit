// Package cli implements the sqit command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"sqit/internal/core"
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
	return core.Open(env.Dir)
}

// ---------------------------------------------------------------------------

func runInit(ctx context.Context, env *Env, args []string) error {
	fs := newFlags("init", env)
	cfg := coreDefaultConfig()
	tables := fs.String("tables", "", "comma-separated list of tables to track")
	fs.StringVar(&cfg.Local.Host, "local-host", cfg.Local.Host, "LOCAL MySQL host")
	fs.IntVar(&cfg.Local.Port, "local-port", cfg.Local.Port, "LOCAL MySQL port")
	fs.StringVar(&cfg.Local.User, "local-user", cfg.Local.User, "LOCAL MySQL user")
	fs.StringVar(&cfg.Local.Database, "local-db", cfg.Local.Database, "LOCAL database name")
	fs.StringVar(&cfg.Test.Host, "test-host", cfg.Test.Host, "TEST MySQL host")
	fs.IntVar(&cfg.Test.Port, "test-port", cfg.Test.Port, "TEST MySQL port")
	fs.StringVar(&cfg.Test.User, "test-user", cfg.Test.User, "TEST MySQL user")
	fs.StringVar(&cfg.Test.Database, "test-db", cfg.Test.Database, "TEST database name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, t := range strings.Split(*tables, ",") {
		if t = strings.TrimSpace(t); t != "" {
			cfg.Tables = append(cfg.Tables, t)
		}
	}
	r, err := coreInit(env.Dir, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Initialized empty sqit repository in %s\n", r.Dir)
	if len(cfg.Tables) == 0 || cfg.Local.Database == "" || cfg.Test.Database == "" {
		fmt.Fprintf(env.Stdout, "Edit %s to set the databases and the tables to track.\n", r.Dir+string(os.PathSeparator)+"config.json")
	}
	return nil
}
