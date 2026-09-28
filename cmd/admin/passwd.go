package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tarkiman/claude-whatsapp/internal/adminauth"
)

// `admin passwd` sets or resets the Admin UI login from the command line. It
// is how the account is created at install time, and the recovery path when
// the password is forgotten: it needs no login because it can only be run on
// the machine, as a user who can already read the credentials file.

func credentialsPath() string {
	if v := os.Getenv("ADMIN_AUTH_FILE"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-whatsapp", "admin.json")
}

type passwdEnv struct {
	path       string
	iterations int // 0 = production default
	in         io.Reader
	out, errw  io.Writer
	tty        bool
	secret     func(prompt string) (string, error) // reads a password without echo
}

func runPasswd(e passwdEnv, args []string) int {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	fs.SetOutput(e.errw)
	user := fs.String("user", "", "username (default: the current one, or \"admin\")")
	fromStdin := fs.Bool("stdin", false, "read the new password from the first line of standard input")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	store := adminauth.Open(e.path, e.iterations)
	if _, err := store.State(); err != nil {
		fmt.Fprintf(e.errw, "note: %v — it will be replaced\n", err)
	}
	name := *user
	if name == "" {
		name = store.Username()
		if name == "" {
			name = "admin"
		}
		if e.tty && !*fromStdin {
			fmt.Fprintf(e.errw, "Username [%s]: ", name)
			if line, _ := bufio.NewReader(e.in).ReadString('\n'); strings.TrimSpace(line) != "" {
				name = strings.TrimSpace(line)
			}
		}
	}
	if err := adminauth.ValidateUsername(name); err != nil {
		fmt.Fprintln(e.errw, "error:", err)
		return 1
	}

	set := func(pw string) error { return store.Set(name, pw) }

	if *fromStdin {
		line, err := bufio.NewReader(e.in).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(e.errw, "error: --stdin needs the password on the first line of standard input")
			return 1
		}
		if err := set(strings.TrimRight(line, "\r\n")); err != nil {
			fmt.Fprintln(e.errw, "error:", err)
			return 1
		}
	} else {
		if !e.tty {
			fmt.Fprintln(e.errw, "error: no terminal to ask on — pipe the password with --stdin")
			return 1
		}
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			pw, err := e.secret("New password (at least 10 characters, 5 different ones): ")
			if err != nil {
				fmt.Fprintln(e.errw, "error:", err)
				return 1
			}
			again, err := e.secret("Repeat the password: ")
			if err != nil {
				fmt.Fprintln(e.errw, "error:", err)
				return 1
			}
			if pw != again {
				lastErr = errors.New("the two passwords differ")
			} else if lastErr = set(pw); lastErr == nil {
				break
			}
			fmt.Fprintln(e.errw, "error:", lastErr)
		}
		if lastErr != nil {
			return 1
		}
	}
	fmt.Fprintf(e.out, "Admin login saved for user %q (%s). Everybody who was signed in has to log in again.\n", name, e.path)
	return 0
}

// ttySecret reads a line without echoing it (stty is available wherever the
// installer's prerequisites — Linux with systemd — are).
func ttySecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	_ = off.Run()
	defer func() {
		on := exec.Command("stty", "echo")
		on.Stdin = os.Stdin
		_ = on.Run()
		fmt.Fprintln(os.Stderr)
	}()
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
