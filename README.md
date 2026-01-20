# gr - Git Remote Helper

A CLI tool for managing pull requests and branches across GitHub, GitLab, and Bitbucket.

## Installation

```bash
go install github.com/simonfxr/gr@latest
```

## Usage

```bash
gr pr list              # List pull requests
gr pr create            # Create a pull request
gr pr view <number>     # View PR details
gr pr merge <number>    # Merge a PR
gr pr close <number>    # Close a PR
gr pr browse [number]   # Open PR in browser

gr branch list          # List remote branches
gr branch rename <old> <new>
gr branch delete <name>
gr branch browse [name] # Open branch in browser
```

## Configuration

gr looks for configuration in these locations (in order):

1. `$GR_CONFIG` (if set, must exist)
2. Platform-specific config directory:
   - Linux: `$XDG_CONFIG_HOME/gr/config.toml` (defaults to `~/.config/gr/config.toml`)
   - macOS: `~/Library/Application Support/gr/config.toml`, then `~/.config/gr/config.toml`
   - Windows: `%APPDATA%\gr\config.toml`

### Config File Format

```toml
[github]
# Option 1: literal token
token = "ghp_xxxxxxxxxxxx"

# Option 2: expand from environment variable
token = "${GITHUB_TOKEN}"

# Option 3: run command to get token
credential_helper = "gh auth token"

[gitlab]
token = "${GITLAB_TOKEN}"
# or use a credential helper:
# credential_helper = "pass show gitlab/token"

[bitbucket]
username = "myuser"                  # required for app password auth
token = "${BITBUCKET_TOKEN}"
# or use a credential helper:
# credential_helper = "pass show bitbucket/token"
```

Note: Use only one of `token` or `credential_helper` per provider. If both are set, `token` takes precedence.

### Token Resolution Order

For each provider, tokens are resolved in this order:

1. Environment variable (`GITHUB_TOKEN`, `GITLAB_TOKEN`, `BITBUCKET_TOKEN`)
2. `token` field in config (with `${VAR}` expansion)
3. `credential_helper` command output (cached for duration of command)

### Credential Helpers

The `credential_helper` field runs a shell command and uses its stdout as the token. Examples:

```toml
# GitHub CLI
credential_helper = "gh auth token"

# 1Password CLI
credential_helper = "op read 'op://Vault/GitLab/token'"

# pass (password-store)
credential_helper = "pass show gitlab/token"

# Bitwarden CLI
credential_helper = "bw get password gitlab-token"
```

If the credential helper exits with non-zero status, its stderr is shown to the user.

## Environment Variables

Without a config file, gr uses these environment variables:

| Provider   | Token Variable      | Username Variable      |
|------------|---------------------|------------------------|
| GitHub     | `GITHUB_TOKEN`      | -                      |
| GitLab     | `GITLAB_TOKEN`      | -                      |
| Bitbucket  | `BITBUCKET_TOKEN`   | `BITBUCKET_USERNAME`   |

## Provider Detection

gr automatically detects the provider from your git remote URL. It supports:

- GitHub (github.com and GitHub Enterprise)
- GitLab (gitlab.com and self-hosted)
- Bitbucket Cloud

## License

MIT
