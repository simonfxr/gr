# Some ideas on how the gr command should work

## CLI COMMANDS

### Pull Request Commands

#### ✅ `gr pr list [OPTIONS]`

List pull requests for the current repository.

**Options:**
- `--state=STATE` - Filter by state: `open` (default), `closed`, `merged`, `all`
- `--author=USER` - Filter by author username
- `--assignee=USER` - Filter by assignee username
- `--base=BRANCH` - Filter by base branch
- `--head=BRANCH` - Filter by head branch
- `--limit=N` - Limit number of results (default: 30)

**Examples:**
```bash
gr pr list                          # List open PRs
gr pr list --state=all              # List all PRs
gr pr list --author=myuser          # List PRs by specific author
gr pr list --base=main --state=open # List open PRs targeting main branch
```

#### ✅ `gr pr create [OPTIONS]`

Create a new pull request for the current branch.

**Options:**
- `--title=TITLE` - PR title (if not provided, will prompt or use commit message)
- `--body=BODY` - PR description/body (if not provided, will prompt or use commit messages)
- `--base=BRANCH` - Target branch (default: repository default branch)
- `--head=BRANCH` - Source branch (default: current branch)
- `--draft` - Create as draft PR
- `--no-edit` - Skip interactive editing of title/body

**Examples:**
```bash
gr pr create                                    # Create PR with interactive prompts
gr pr create --title="Fix user login bug"      # Create PR with specific title
gr pr create --draft --base=develop            # Create draft PR targeting develop
gr pr create --title="Feature" --body="Desc"  # Create PR with title and body
```

#### ✅ `gr pr view <PR_NUMBER>`

View details of a specific pull request.

**Examples:**
```bash
gr pr view 123                      # View PR #123
```

#### `gr pr checkout <PR_NUMBER>`

Checkout the branch for a specific pull request locally.

**Examples:**
```bash
gr pr checkout 123                  # Checkout PR #123 branch
```

#### ✅ `gr pr merge <PR_NUMBER> [OPTIONS]`

Merge a pull request.

**Options:**
- `--method=METHOD` - Merge method: `merge`, `squash`, `rebase`, if not given, the default configured for repo is used 
- `--delete-branch` - Delete the source branch after merge

**Examples:**
```bash
gr pr merge 123                     # Merge PR #123
gr pr merge 123 --method=squash     # Squash merge PR #123
```

#### ✅ `gr pr close <PR_NUMBER> [OPTIONS]`

Close a pull request without merging.

**Options:**
- `--delete-branch` - Delete the source branch after closing
- `--json` - Output as JSON

**Examples:**
```bash
gr pr close 123                      # Close PR #123
gr pr close 123 --delete-branch      # Close and delete source branch
```

### Branch Commands

#### ✅ `gr branch list [OPTIONS]`

List remote branches for the current repository via provider APIs.

**Options:**
- `--pattern=PATTERN` - Filter branches by glob pattern
- `--sort=FIELD` - Sort by: `name` (default), `date`, `author`
- `--json` - Output as JSON

**Examples:**
```bash
gr branch list                             # List remote branches
gr branch list --pattern="feature/*"       # List feature branches
gr branch list --sort=date                 # Sort by last commit date
gr branch list --json                      # JSON output
```

#### ✅ `gr branch delete <BRANCH_NAME> [OPTIONS]`

Delete a branch both locally and remotely.

**Options:**
- `--force` - Force delete even if not merged
- `--local-only` - Delete local branch only, keep remote
- `--remote-only` - Delete remote branch only, keep local
- `--dry-run` - Show what would be deleted without doing it

**Examples:**
```bash
gr branch delete feature/old-feature     # Delete branch locally and remotely
gr branch delete feature/test --force    # Force delete unmerged branch
gr branch delete feature/temp --local-only # Delete local branch only
gr branch delete --dry-run feature/*     # Preview what would be deleted
```

#### ✅ `gr branch rename <OLD_NAME> <NEW_NAME> [OPTIONS]`

Rename a branch using forge API calls to rename the actual remote branch.

**Options:**
- `--local-only` - Rename local branch only, don't update remote
- `--no-update-prs` - Do not retarget open PRs/MRs to the new branch name
- `--yes` - Skip confirmation prompts

**Examples:**
```bash
gr branch rename old-name new-name          # Rename branch locally and remotely
gr branch rename feature/old feature/new   # Rename with path structure
gr branch rename temp-branch final --local-only # Rename local branch only
```

**Note:** The `rename` command uses provider API calls (GitHub/GitLab/Bitbucket) to actually rename the remote branch, not just update local tracking. This operation will:
- Rename the remote branch via API
- Update local tracking branch
- Update any open pull requests to reference the new branch name
- Handle branch protection rules and access permissions

## Implementation notes

### Multi-Provider Support

The CLI should automatically detect which git hosting provider is being used by parsing the git remote URLs:

- **GitHub**: `github.com` or `*.github.com`
- **GitLab**: `gitlab.com` or self-hosted GitLab instances
- **Bitbucket**: `bitbucket.org` or Bitbucket Server instances

### Authentication

Authentication is handled via environment variables. Users must set the appropriate tokens:

- `GITHUB_TOKEN` - GitHub Personal Access Token or Fine-grained Personal Access Token
- `GITLAB_TOKEN` - GitLab Personal Access Token or Project Access Token  
- `BITBUCKET_TOKEN` - Bitbucket App Password or Personal Access Token

**Required token permissions:**
- GitHub: `repo`, `pull_requests` scopes
- GitLab: `api`, `read_repository`, `write_repository` scopes
- Bitbucket: `repositories:read`, `pullrequests:write` permissions

### Repository Detection

1. **Git Remote Parsing**: Parse `.git/config` to extract remote URLs
2. **Provider Detection**: Match URL patterns to identify hosting provider
3. **Repository Info**: Extract owner/repo from remote URL
4. **Multiple Remotes**: Handle cases with multiple remotes (prefer `origin`, then `upstream`)

### Error Handling

- **No Token**: Clear error message directing user to set appropriate env var
- **Invalid Token**: Authentication failure with token validation guidance  
- **No Git Repo**: Detect when not in a git repository and provide helpful message
- **Unknown Provider**: Handle unsupported git hosting providers gracefully
- **Existing PR**: When creating PR, check if one already exists for the branch (as specified)
- **Network Issues**: Retry logic for API calls with exponential backoff

### API Client Configuration

- **GitHub**: Use `github.com/google/go-github/v74` with token authentication
- **GitLab**: Use `gitlab.com/gitlab-org/api/client-go` with private token
- **Bitbucket**: Use `github.com/ktrysmt/go-bitbucket` with app password auth

### Output Formatting

Consistent output format across all providers:
- **PR List**: Table format with ID, title, author, state, created date
- **PR Details**: Structured display with title, body, metadata, review status
- **Success Messages**: Clear confirmation of actions taken
- **Error Messages**: Actionable error descriptions with suggested fixes

### Branch Detection

- **Current Branch**: Use `git symbolic-ref --short HEAD` or equivalent
- **Default Branch**: Query API to get repository default branch for PR creation
- **Branch Validation**: Verify source branch exists and has commits ahead of target
