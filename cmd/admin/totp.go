package main

import (
	"fmt"
	"io"

	"github.com/tarkiman/claude-whatsapp/internal/adminauth"
)

type totpEnv struct {
	path      string
	out, errw io.Writer
}

// runTOTP implements `admin totp status|disable` — the recovery path for an
// operator who lost their authenticator device and can no longer get past
// the code prompt at login. Like `admin passwd`, it needs no login of its
// own because it can only be run on the machine itself, by a user who can
// already read the credentials file.
func runTOTP(e totpEnv, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(e.errw, "usage: admin totp status|disable")
		return 2
	}
	store := adminauth.Open(e.path, 0)
	switch args[0] {
	case "status":
		exists, err := store.State()
		if err != nil {
			fmt.Fprintln(e.errw, "error:", err)
			return 1
		}
		if !exists {
			fmt.Fprintln(e.errw, "error: no admin account exists yet")
			return 1
		}
		if store.TOTPEnabled() {
			fmt.Fprintln(e.out, "2FA is ON.")
		} else {
			fmt.Fprintln(e.out, "2FA is OFF.")
		}
		return 0
	case "disable":
		exists, err := store.State()
		if err != nil {
			fmt.Fprintln(e.errw, "error:", err)
			return 1
		}
		if !exists {
			fmt.Fprintln(e.errw, "error: no admin account exists yet")
			return 1
		}
		if !store.TOTPEnabled() {
			fmt.Fprintln(e.out, "2FA was already off — nothing to do.")
			return 0
		}
		if err := store.DisableTOTP(); err != nil {
			fmt.Fprintln(e.errw, "error:", err)
			return 1
		}
		fmt.Fprintln(e.out, "2FA disabled. Sign in with just your password — re-enable it from the Admin login card if you want to set up a new authenticator.")
		return 0
	default:
		fmt.Fprintln(e.errw, "usage: admin totp status|disable")
		return 2
	}
}
