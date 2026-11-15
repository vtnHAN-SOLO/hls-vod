// Command useradmin manages api users. There is no sign-up endpoint: every
// account is created here, with DATABASE_URL.
//
//	useradmin add [--role user|admin] <username>
//	useradmin set-password <username>      (also ends every session of that user)
//	useradmin set-role <username> <user|admin>
//	useradmin list
//
// The password is never an argument or an environment variable, where it
// would land in shell history or /proc. On a terminal it is prompted for
// twice with echo off; otherwise one line is read from stdin, e.g.
// `printf '%s\n' "$pw" | useradmin add alice` from a secret store.
//
// Exit codes: 0 done, 1 failed, 2 bad usage.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

const usage = `usage:
  useradmin add [--role user|admin] <username>
  useradmin set-password <username>
  useradmin set-role <username> <user|admin>
  useradmin list

The password is read from the terminal (asked twice, not echoed) or, when
stdin is not a terminal, from the first line of stdin. Never from argv or env.`

var errUsage = errors.New("bad usage")

// userStore is the slice of the store useradmin needs.
type userStore interface {
	CreateUser(ctx context.Context, username, passwordHash string, role model.Role) (model.User, error)
	SetPasswordHash(ctx context.Context, username, passwordHash string) error
	SetRole(ctx context.Context, username string, role model.Role) error
	ListUsers(ctx context.Context) ([]model.User, error)
}

// passwordSource asks for a new password.
type passwordSource func() (string, error)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(args []string) int {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load[config.UserAdmin]()
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	defer st.Close()
	return run(ctx, args, st, auth.NewHasher(1), stdinPassword(os.Stdin, os.Stderr), os.Stdout, os.Stderr)
}

func run(ctx context.Context, args []string, st userStore, hasher *auth.Hasher, password passwordSource, out, errOut io.Writer) int {
	err := dispatch(ctx, args, st, hasher, password, out)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(errOut, "%v\n\n%s\n", err, usage)
		return 2
	default:
		_, _ = fmt.Fprintf(errOut, "error: %v\n", err)
		return 1
	}
}

func dispatch(ctx context.Context, args []string, st userStore, hasher *auth.Hasher, password passwordSource, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: no command", errUsage)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "add":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		role := fs.String("role", "user", "user or admin")
		if err := fs.Parse(rest); err != nil {
			return fmt.Errorf("%w: %w", errUsage, err)
		}
		name, err := oneUsername(fs.Args())
		if err != nil {
			return err
		}
		r, err := parseRole(*role)
		if err != nil {
			return err
		}
		hash, err := newPasswordHash(ctx, hasher, password)
		if err != nil {
			return err
		}
		u, err := st.CreateUser(ctx, name, hash, r)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "created %s (role %s, id %s)\n", u.Username, u.Role, u.ID)
	case "set-password":
		name, err := oneUsername(rest)
		if err != nil {
			return err
		}
		hash, err := newPasswordHash(ctx, hasher, password)
		if err != nil {
			return err
		}
		if err := st.SetPasswordHash(ctx, name, hash); err != nil {
			return userErr(name, err)
		}
		_, _ = fmt.Fprintf(out, "password changed for %s; every session of theirs is revoked\n", name)
	case "set-role":
		if len(rest) != 2 {
			return fmt.Errorf("%w: set-role takes <username> <user|admin>", errUsage)
		}
		name, err := oneUsername(rest[:1])
		if err != nil {
			return err
		}
		r, err := parseRole(rest[1])
		if err != nil {
			return err
		}
		if err := st.SetRole(ctx, name, r); err != nil {
			return userErr(name, err)
		}
		_, _ = fmt.Fprintf(out, "%s is now %s (access tokens already issued keep the old role for up to 15 min)\n", name, r)
	case "list":
		if len(rest) != 0 {
			return fmt.Errorf("%w: list takes no arguments", errUsage)
		}
		users, err := st.ListUsers(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "USERNAME\tROLE\tCREATED")
		for _, u := range users {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", u.Username, u.Role, u.CreatedAt.UTC().Format("2006-01-02 15:04"))
		}
		return tw.Flush()
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, cmd)
	}
	return nil
}

// oneUsername accepts exactly one argument: anything more could be a
// password typed on the command line, and is refused.
func oneUsername(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%w: expected exactly one <username> (the password is never an argument)", errUsage)
	}
	name := strings.ToLower(strings.TrimSpace(args[0]))
	if auth.CheckUsername(name) != nil {
		return "", fmt.Errorf("%w: username %q must be 1-64 characters of a-z, 0-9, '.', '_', '-', starting with a letter or digit", errUsage, args[0])
	}
	return name, nil
}

func parseRole(s string) (model.Role, error) {
	r := model.Role(s)
	if !r.Valid() {
		return "", fmt.Errorf("%w: role must be user or admin, got %q", errUsage, s)
	}
	return r, nil
}

func userErr(name string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no user %q", name)
	}
	return err
}

func newPasswordHash(ctx context.Context, hasher *auth.Hasher, password passwordSource) (string, error) {
	pw, err := password()
	if err != nil {
		return "", err
	}
	if err := auth.CheckPassword(pw); err != nil {
		return "", err
	}
	return hasher.Hash(ctx, pw)
}

// stdinPassword prompts twice on a terminal, with echo off; otherwise it
// reads the first line of in.
func stdinPassword(in *os.File, prompt io.Writer) passwordSource {
	return func() (string, error) {
		fd := int(in.Fd()) // #nosec G115 -- a file descriptor fits in an int
		if !term.IsTerminal(fd) {
			return readLine(in)
		}
		_, _ = fmt.Fprint(prompt, "Password: ")
		first, err := term.ReadPassword(fd)
		_, _ = fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		_, _ = fmt.Fprint(prompt, "Repeat password: ")
		second, err := term.ReadPassword(fd)
		_, _ = fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		if string(first) != string(second) {
			return "", errors.New("the passwords do not match")
		}
		return string(first), nil
	}
}

// readLine returns the first line of r without its line ending.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 8<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading password from stdin: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("no password on stdin (pipe one line, or run on a terminal to be prompted)")
	}
	return line, nil
}
