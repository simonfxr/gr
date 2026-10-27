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
gr pr view [PR]         # View PR details
gr pr merge [PR]        # Merge a PR
gr pr close [PR]        # Close a PR
gr pr browse [PR]       # Open PR in browser

gr pr todo              # What is left: open tasks + unresolved comments (Bitbucket)
gr pr comments          # List review comments
gr pr addcomment <file:line> <text>
gr pr addcomments --from-json <file|->
gr pr reply <comment-id> <text>
gr pr resolve <comment-id>

gr pr tasks             # List PR tasks (Bitbucket)
gr pr addtask <text>    # Create a PR task
gr pr resolvetask <id>  # Resolve (or --reopen) a PR task

gr branch list          # List remote branches
gr branch rename <old> <new>
gr branch delete <name>
gr branch browse [name] # Open branch in browser
```

### Selecting a pull request

Every command that operates on a single PR accepts:

- `-n/--number` with a number (`42`, `#42`) or the PR's web URL. URLs must
  belong to the current repository.
- `-b/--branch BRANCH` to use the open PR of that branch.
- Neither: the open PR of the current branch.

`view`, `browse`, `merge`, `close` and `checkout` also take the PR as a
positional argument (`gr pr view 42`).

### Merging

```bash
gr pr merge --method squash --delete-branch
```

`--method` is `merge`, `squash` or `rebase` (rebase and fast-forward, no merge
commit); without it the repository's default applies. GitLab has no per-merge
rebase: its project setting decides between merge commits and fast-forward.
`--delete-branch`/`--keep-branch` override the PR's own setting (PRs created by
`gr pr create` are set to delete their branch). A merge that Bitbucket queues
is reported as not merged yet.

### Text input

`addcomment`, `reply` and `addtask` take their text as an argument or via
`--body-file FILE` (`-` reads stdin), as does `pr create` for the description.
This avoids shell quoting for multi-line Markdown and code:

```bash
gr pr reply 1234 --body-file - <<'MSG'
Fixed, now uses `defineProps<{ foo?: string }>()`.
MSG
```

`addcomments --from-json -` reads the comment array from stdin.

### Scripting and agents

- **Exit status:** `0` success, `1` error, `2` invalid usage, `3` PR/comment/task
  not found or PR selection ambiguous. Empty listings exit `0`.
- **JSON:** `--json` is available on every command that produces data; field
  names are snake_case. With `--json`, errors are written to stderr as
  `{"error": "...", "kind": "usage|not_found|ambiguous|error", "exit_code": N}`;
  ambiguous PR selection adds `"candidates"` (the matching PRs).
- **Batch comments:** `addcomments --json` prints one result per input item
  (`{path, line, ok, comment | error}`) and exits `1` if any item failed.
- **Tables:** when stdout is not a terminal, tables are printed as CSV.

### PR todo list

```bash
gr pr todo            # current branch's PR
gr pr todo -n 1282 --json
```

Lists open tasks and unresolved top-level comment threads, oldest first. Each
item has `kind` (`task`/`comment`), `id`, `author`, `body`, `created_at` and,
when known, the code location (`path`, `line`). Tasks attached to a comment
inherit its location and carry `comment_id` and `pending`; comments include
their thread `replies`. Close items with `gr pr resolvetask ID` or
`gr pr resolve ID`.

### Bitbucket listings

```bash
gr pr list --state merged --author "Simon Reiser" --limit 20
gr pr list --state all --head "feature/my-change" --limit 10
gr branch list --pattern "feature/*" --author "Simon" --since 3w --sort date
```

Bitbucket PRs are listed newest-created first. `--limit` counts matching PRs
(default 30; zero or negative means unlimited), and pagination stops once enough
matches are found. `--state all` includes open, merged, declined, and superseded
PRs; `closed` includes the latter three.

PR authors can be specified by UUID, account ID, nickname, or full display name.
Names use case-insensitive exact matching; branch authors use case-insensitive
substring matching. Listings request only the fields needed for output and push
filters to Bitbucket where possible, retaining local checks for matching semantics.
If Bitbucket rejects a name/commit-metadata filter, listing falls back to local
filtering without dropping the other supported filters.

### Bitbucket PR tasks

```bash
gr pr tasks                         # tasks of the open PR for the current branch
gr pr tasks -n 1282 --state open    # open|resolved|all (default all)
gr pr tasks -b feature/x --json
gr pr addtask "Fix UI"                # new task on the current branch's PR
gr pr addtask -c 1234 "Cleanup types" # attach the task to comment 1234
gr pr addtask --pending "Fix UI"      # part of your unsubmitted review
gr pr resolvetask 5678
gr pr resolvetask --reopen 5678
```

Table output shows task ID, resolved/pending state, creator, creation time,
attached comment ID, and text; `--json` emits the same fields.

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
