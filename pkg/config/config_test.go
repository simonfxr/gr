package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[gitlab]
token = "default"

[hosts."Git.Example.com"]
provider = "gitlab"
api_url = "https://git.example.com/gitlab/api/v4"
token = "${WORK_TOKEN}"
username = "me"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GR_CONFIG", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	hc, ok := cfg.Host("git.example.com")
	if !ok {
		t.Fatalf("host not found: %+v", cfg.Hosts)
	}
	if hc.Provider != "gitlab" || hc.APIURL != "https://git.example.com/gitlab/api/v4" ||
		hc.Token != "${WORK_TOKEN}" || hc.Username != "me" || cfg.GitLab.Token != "default" {
		t.Errorf("host config = %+v", hc)
	}
	if _, ok := cfg.Host("other"); ok {
		t.Error("unexpected host")
	}
	var nilCfg *Config
	if _, ok := nilCfg.Host("x"); ok {
		t.Error("nil config has no hosts")
	}
}
