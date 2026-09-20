package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"sqit/internal/mysql"
	"sqit/internal/repo"
)

// prompter asks the user for connection details when `sqit init` is run
// without the corresponding flags on a terminal.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	tty bool
}

func newPrompter(out io.Writer) *prompter {
	return &prompter{in: bufio.NewReader(os.Stdin), out: out, tty: term.IsTerminal(int(os.Stdin.Fd()))}
}

func (p *prompter) ask(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	line, _ := p.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func (p *prompter) askInt(label string, def int) int {
	for {
		s := p.ask(label, strconv.Itoa(def))
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
		fmt.Fprintln(p.out, "  please enter a port number")
	}
}

func (p *prompter) askYesNo(label string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	s := strings.ToLower(p.ask(label, d))
	switch s {
	case "y", "yes", "i", "igen":
		return true
	case "n", "no", "nem":
		return false
	}
	return def
}

func (p *prompter) askPassword(label string) string {
	fmt.Fprintf(p.out, "%s (hidden, leave empty to fill sqit.env later): ", label)
	if p.tty {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(p.out)
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	line, _ := p.in.ReadString('\n')
	return strings.TrimSpace(line)
}

// interactiveInit fills in the connection settings that were not given as
// flags. It returns the passwords entered (to be written to sqit.env).
func interactiveInit(cfg *repo.Config, out io.Writer, set map[string]bool) (localPw, testPw string) {
	p := newPrompter(out)
	fmt.Fprintln(out, "Configuring the LOCAL database (your working tree – sqit reads and writes it):")
	if !set["local-host"] {
		cfg.Local.Host = p.ask("  host", cfg.Local.Host)
	}
	if !set["local-port"] {
		cfg.Local.Port = p.askInt("  port", cfg.Local.Port)
	}
	if !set["local-user"] {
		cfg.Local.User = p.ask("  user", cfg.Local.User)
	}
	if !set["local-db"] {
		for cfg.Local.Database == "" {
			cfg.Local.Database = p.ask("  database", "")
		}
	}
	localPw = p.askPassword("  password")

	fmt.Fprintln(out, "Configuring the UPSTREAM / TEST database (sqit only reads it):")
	if !set["test-host"] {
		cfg.Test.Host = p.ask("  host", cfg.Local.Host)
	}
	if !set["test-port"] {
		cfg.Test.Port = p.askInt("  port", cfg.Local.Port)
	}
	if !set["test-user"] {
		cfg.Test.User = p.ask("  user", cfg.Local.User)
	}
	if !set["test-db"] {
		for cfg.Test.Database == "" || cfg.Test.Database == cfg.Local.Database && cfg.Test.Host == cfg.Local.Host && cfg.Test.Port == cfg.Local.Port {
			cfg.Test.Database = p.ask("  database", "")
			if cfg.Test.Database == cfg.Local.Database && cfg.Test.Host == cfg.Local.Host && cfg.Test.Port == cfg.Local.Port {
				fmt.Fprintln(out, "  the upstream must be a different database than LOCAL")
			}
		}
	}
	if cfg.Test.User == cfg.Local.User && cfg.Test.Host == cfg.Local.Host && cfg.Test.Port == cfg.Local.Port && localPw != "" {
		if p.askYesNo("  same password as LOCAL?", true) {
			testPw = localPw
		} else {
			testPw = p.askPassword("  password")
		}
	} else {
		testPw = p.askPassword("  password")
	}
	if !set["local-only"] {
		cfg.LocalOnly = p.askYesNo("Refuse upstream hosts other than localhost (local-only mode)?", isLoopbackHost(cfg.Test.Host))
	}
	return localPw, testPw
}

func isLoopbackHost(h string) bool {
	h = strings.ToLower(h)
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == ""
}

// tryConnect reports whether the connection works, as a one-line message.
func tryConnect(label string, c repo.Connection) string {
	dsn, err := c.DSN()
	if err != nil {
		return fmt.Sprintf("%s: %v", label, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := mysql.Connect(ctx, dsn)
	if err != nil {
		return fmt.Sprintf("%s: connection FAILED: %v", label, err)
	}
	defer sess.Close()
	tables, err := mysql.ListTables(ctx, sess.Conn())
	if err != nil {
		return fmt.Sprintf("%s: connected, but listing tables failed: %v", label, err)
	}
	return fmt.Sprintf("%s: connected (%s), %d table(s)", label, c.Describe(), len(tables))
}
