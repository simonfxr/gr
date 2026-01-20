package auth

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/simonfxr/gr/pkg/config"
)

var (
	helperCache   = make(map[string]string)
	helperCacheMu sync.Mutex
)

// GetToken resolves a token using the resolution chain:
// 1. Environment variable (e.g. GITHUB_TOKEN)
// 2. Config token (expanded via os.ExpandEnv, supports "${VAR}")
// 3. Credential helper command (cached per command string)
func GetToken(cfg *config.ProviderConfig, defaultEnv string) (string, error) {
	if token := strings.TrimSpace(os.Getenv(defaultEnv)); token != "" {
		return token, nil
	}
	if cfg != nil && cfg.Token != "" {
		if token := strings.TrimSpace(os.ExpandEnv(cfg.Token)); token != "" {
			return token, nil
		}
	}
	if cfg != nil && cfg.CredentialHelper != "" {
		token, err := runHelperCached(cfg.CredentialHelper)
		if err != nil {
			return "", err
		}
		if token != "" {
			return token, nil
		}
	}
	return "", nil
}

// GetUsername resolves username from env var or config (with env expansion).
func GetUsername(cfg *config.ProviderConfig, defaultEnv string) string {
	if user := strings.TrimSpace(os.Getenv(defaultEnv)); user != "" {
		return user
	}
	if cfg != nil && cfg.Username != "" {
		if user := strings.TrimSpace(os.ExpandEnv(cfg.Username)); user != "" {
			return user
		}
	}
	return ""
}

func runHelperCached(cmd string) (string, error) {
	helperCacheMu.Lock()
	defer helperCacheMu.Unlock()

	if token, ok := helperCache[cmd]; ok {
		return token, nil
	}

	token, err := runHelper(cmd)
	if err != nil {
		return "", err
	}
	helperCache[cmd] = token
	return token, nil
}

func runHelper(cmd string) (string, error) {
	c := exec.Command("sh", "-c", cmd)
	out, err := c.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("credential helper %q failed: %s", cmd, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("credential helper %q failed: %w", cmd, err)
	}
	return strings.TrimSpace(string(out)), nil
}
