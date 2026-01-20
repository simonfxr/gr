package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

type Config struct {
	GitHub    ProviderConfig `toml:"github"`
	GitLab    ProviderConfig `toml:"gitlab"`
	Bitbucket ProviderConfig `toml:"bitbucket"`
}

type ProviderConfig struct {
	Token            string `toml:"token"` // literal or "${ENV_VAR}"
	CredentialHelper string `toml:"credential_helper"`
	Username         string `toml:"username"`
}

// Load reads config from standard locations. Returns empty config if no file found.
// Returns error if $GR_CONFIG is set but file doesn't exist.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return &Config{}, nil
	}
	cfg := &Config{}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	return cfg, nil
}

func configPath() (string, error) {
	if p := os.Getenv("GR_CONFIG"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("$GR_CONFIG set but file not found: %s", p)
		}
		return p, nil
	}
	for _, dir := range configDirs() {
		if p := filepath.Join(dir, "gr", "config.toml"); fileExists(p) {
			return p, nil
		}
	}
	return "", nil
}

func configDirs() []string {
	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return []string{appData}
		}
		return nil
	case "darwin":
		home, _ := os.UserHomeDir()
		if home == "" {
			return nil
		}
		return []string{
			filepath.Join(home, "Library", "Application Support"),
			filepath.Join(home, ".config"), // fallback for unix-style
		}
	default: // linux, etc
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return []string{xdg}
		}
		if home, _ := os.UserHomeDir(); home != "" {
			return []string{filepath.Join(home, ".config")}
		}
		return nil
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
