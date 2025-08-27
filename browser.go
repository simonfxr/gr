package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser opens the provided URL using the system browser.
// It honors the $BROWSER environment variable if set. When $BROWSER
// contains additional arguments, they are parsed by splitting on
// whitespace. If unset, a sensible OS-specific default opener is used.
func OpenBrowser(url string) error {
	u := strings.TrimSpace(url)
	if u == "" {
		return errors.New("empty URL")
	}

	// If BROWSER is set, use it
	if b := strings.TrimSpace(os.Getenv("BROWSER")); b != "" {
		parts := strings.Fields(b)
		cmd := exec.Command(parts[0], append(parts[1:], u)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Start()
	}

	// Fall back to OS-specific openers
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		// Try a few common Linux/BSD openers
		candidates := [][]string{
			{"xdg-open", u},
			{"gio", "open", u},
			{"gnome-open", u},
			{"kde-open", u},
		}
		var lastErr error
		for _, c := range candidates {
			if err := exec.Command(c[0], c[1:]...).Start(); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
		if lastErr != nil {
			return fmt.Errorf("failed to open browser; set $BROWSER or install xdg-open: %w", lastErr)
		}
		return errors.New("failed to open browser; set $BROWSER or install xdg-open")
	}
}
