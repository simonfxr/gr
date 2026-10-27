package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/alexflint/go-arg"

	"github.com/simonfxr/gr/pkg/config"
	"github.com/simonfxr/gr/pkg/provider"
)

// Top-level CLI
type Args struct {
	Chdir  string     `arg:"-C,--chdir" help:"path to repo (like git -C DIR)"`
	PR     *PRCmd     `arg:"subcommand:pr" help:"pull request commands"`
	Branch *BranchCmd `arg:"subcommand:branch" help:"branch commands"`
}

// `gr pr` group and its subcommands
type PRCmd struct {
	List        *PRListCmd        `arg:"subcommand:list" help:"list pull requests"`
	Create      *PRCreateCmd      `arg:"subcommand:create" help:"create a pull request"`
	View        *PRViewCmd        `arg:"subcommand:view" help:"view a pull request"`
	Browse      *PRBrowseCmd      `arg:"subcommand:browse" help:"open a pull request in the browser"`
	Checkout    *PRCheckoutCmd    `arg:"subcommand:checkout" help:"checkout a pull request branch (not implemented yet)"`
	Merge       *PRMergeCmd       `arg:"subcommand:merge" help:"merge a pull request"`
	Close       *PRCloseCmd       `arg:"subcommand:close" help:"close a pull request"`
	Todo        *PRTodoCmd        `arg:"subcommand:todo" help:"list what is left on a PR: open tasks and unresolved comments"`
	Comments    *PRCommentsCmd    `arg:"subcommand:comments" help:"show review comments for a pull/merge request"`
	AddComment  *PRAddCommentCmd  `arg:"subcommand:addcomment" help:"add an inline review comment to a pull request"`
	AddComments *PRAddCommentsCmd `arg:"subcommand:addcomments" help:"add multiple inline comments from a JSON file"`
	Reply       *PRReplyCmd       `arg:"subcommand:reply" help:"reply to a PR comment"`
	Resolve     *PRResolveCmd     `arg:"subcommand:resolve" help:"resolve a PR comment"`
	Tasks       *PRTasksCmd       `arg:"subcommand:tasks" help:"list tasks of a pull request"`
	AddTask     *PRAddTaskCmd     `arg:"subcommand:addtask" help:"create a task on a pull request"`
	ResolveTask *PRResolveTaskCmd `arg:"subcommand:resolvetask" help:"resolve (or reopen) a pull request task"`
}

// Commands targeting a single PR embed PRSelector (-n/--number, -b/--branch).
// Without either, the open PR of the current branch is used.

type PRListCmd struct {
	State    string `arg:"--state" help:"filter by state: open, closed (without merging), merged, all" default:"open"`
	Author   string `arg:"--author" help:"filter by author username"`
	Assignee string `arg:"--assignee" help:"filter by assignee username"`
	Base     string `arg:"--base" help:"filter by base branch"`
	Head     string `arg:"--head" help:"filter by head branch"`
	Limit    int    `arg:"--limit" help:"limit number of results" default:"30"`
	JSON     bool   `arg:"--json" help:"output as JSON"`
}

type PRCreateCmd struct {
	Title              string `arg:"--title" help:"PR title"`
	Body               string `arg:"--body" help:"PR description/body"`
	BodyFile           string `arg:"--body-file" help:"read the PR description from FILE ('-' for stdin)"`
	Base               string `arg:"--base" help:"target branch (default: repo default)"`
	Head               string `arg:"--head" help:"source branch (default: current)"`
	Draft              bool   `arg:"--draft" help:"create as draft PR"`
	Edit               bool   `arg:"--edit" help:"open $VISUAL or $EDITOR to edit title/body (uses PR_EDITMSG in gitdir)"`
	NoEdit             bool   `arg:"--no-edit" help:"skip interactive editing"`
	NoSquash           bool   `arg:"--no-squash" help:"do not squash commits when merging (default: squash)"`
	NoDeleteAfterMerge bool   `arg:"--no-delete-after-merge" help:"keep source branch after merge (default: delete)"`
	JSON               bool   `arg:"--json" help:"output as JSON"`
}

type PRViewCmd struct {
	PR string `arg:"positional" help:"pull request number or URL"`
	PRSelector
	JSON bool `arg:"--json" help:"output as JSON"`
}

type PRBrowseCmd struct {
	PR string `arg:"positional" help:"pull request number or URL"`
	PRSelector
}

type PRCheckoutCmd struct {
	PR string `arg:"positional" help:"pull request number or URL"`
	PRSelector
}

type PRMergeCmd struct {
	PR string `arg:"positional" help:"pull request number or URL"`
	PRSelector
	Method       string `arg:"--method" help:"merge method: merge, squash or rebase (default: the repository's; GitLab: no rebase)"`
	DeleteBranch bool   `arg:"--delete-branch" help:"delete the source branch after merging"`
	KeepBranch   bool   `arg:"--keep-branch" help:"keep the source branch, even if the PR is set to delete it"`
	JSON         bool   `arg:"--json" help:"output as JSON"`
}

type PRCloseCmd struct {
	PR string `arg:"positional" help:"pull request number or URL"`
	PRSelector
	DeleteBranch bool `arg:"--delete-branch" help:"delete source branch after closing"`
	JSON         bool `arg:"--json" help:"output as JSON"`
}

type PRTodoCmd struct {
	PRSelector
	JSON bool `arg:"--json" help:"output as JSON"`
}

type PRCommentsCmd struct {
	PRSelector
	JSON bool `arg:"--json" help:"output as JSON"`
}

type PRAddCommentCmd struct {
	PRSelector
	Location string `arg:"positional,required" help:"file:line location for the inline comment"`
	Body     string `arg:"positional" help:"comment text (or use --body-file)"`
	BodyFile string `arg:"--body-file" help:"read the comment text from FILE ('-' for stdin)"`
	JSON     bool   `arg:"--json" help:"output as JSON"`
}

type PRAddCommentsCmd struct {
	PRSelector
	FromJSON string `arg:"--from-json,required" help:"JSON file ('-' for stdin) with comments array [{path, line, body}, ...]"`
	JSON     bool   `arg:"--json" help:"output per-comment results as JSON"`
}

type PRReplyCmd struct {
	PRSelector
	CommentID int    `arg:"positional,required" help:"comment ID to reply to"`
	Body      string `arg:"positional" help:"reply text (or use --body-file)"`
	BodyFile  string `arg:"--body-file" help:"read the reply text from FILE ('-' for stdin)"`
	JSON      bool   `arg:"--json" help:"output as JSON"`
}

type PRResolveCmd struct {
	PRSelector
	CommentID int  `arg:"positional,required" help:"comment ID to resolve"`
	JSON      bool `arg:"--json" help:"output as JSON"`
}

type PRTasksCmd struct {
	PRSelector
	State string `arg:"--state" help:"filter by state: open, resolved, all" default:"all"`
	JSON  bool   `arg:"--json" help:"output as JSON"`
}

type PRAddTaskCmd struct {
	PRSelector
	CommentID int    `arg:"--comment,-c" help:"attach the task to this comment ID"`
	Pending   bool   `arg:"--pending" help:"create as pending (part of your unsubmitted review)"`
	Body      string `arg:"positional" help:"task text (or use --body-file)"`
	BodyFile  string `arg:"--body-file" help:"read the task text from FILE ('-' for stdin)"`
	JSON      bool   `arg:"--json" help:"output as JSON"`
}

type PRResolveTaskCmd struct {
	PRSelector
	TaskID int  `arg:"positional,required" help:"task ID"`
	Reopen bool `arg:"--reopen" help:"mark the task as unresolved instead"`
	JSON   bool `arg:"--json" help:"output as JSON"`
}

// Branch command group
type BranchCmd struct {
	Rename *BranchRenameCmd `arg:"subcommand:rename" help:"rename a branch locally/remotely"`
	Delete *BranchDeleteCmd `arg:"subcommand:delete" help:"delete a branch locally/remotely"`
	List   *BranchListCmd   `arg:"subcommand:list" help:"list branches"`
	Browse *BranchBrowseCmd `arg:"subcommand:browse" help:"open a branch view in the browser"`
}

type BranchRenameCmd struct {
	Old         string `arg:"positional,required" help:"old branch name"`
	New         string `arg:"positional,required" help:"new branch name"`
	LocalOnly   bool   `arg:"--local-only" help:"rename only locally, do not touch remote"`
	NoUpdatePRs bool   `arg:"--no-update-prs" help:"do not retarget open PRs/MRs to new branch name"`
	Force       bool   `arg:"--force" help:"proceed even if open MRs use the source branch (GitLab). Equivalent to --no-update-prs; may orphan MR source branches."`
}

type BranchDeleteCmd struct {
	Name       string `arg:"positional,required" help:"branch name to delete"`
	LocalOnly  bool   `arg:"--local-only" help:"delete local branch only"`
	RemoteOnly bool   `arg:"--remote-only" help:"delete remote branch only"`
	Force      bool   `arg:"--force" help:"force deletion (skip safety checks where supported)"`
	DryRun     bool   `arg:"--dry-run" help:"show actions without performing them"`
}

type BranchListCmd struct {
	Pattern string `arg:"--pattern" help:"filter branches by glob pattern (e.g., feature/*)"`
	Sort    string `arg:"--sort" help:"sort by: name (default), date, author"`
	Author  string `arg:"--author" help:"filter by author (case-insensitive substring)"`
	Since   string `arg:"--since" help:"filter by max age of last commit (e.g., 72h, 10d, 3w)"`
	JSON    bool   `arg:"--json" help:"output as JSON"`
}

type BranchBrowseCmd struct {
	Name string `arg:"positional" help:"branch name (optional); defaults to current branch"`
}

func (Args) Description() string {
	return "gr - git remote PR helper"
}

func (Args) Epilogue() string {
	return "Exit status: 0 success, 1 error, 2 invalid usage or unsupported by the provider, 3 pull request/comment/task not found or ambiguous, 4 missing or rejected credentials, 130 interrupted.\n" +
		"With --json, errors are printed to stderr as JSON: {error, kind, exit_code, candidates}."
}

// lazy wraps a computation and ensures it runs at most once per process
// (thread-safe via sync.Once). Useful for deferring detection until needed.
func lazy[T any](f func() (T, error)) func() (T, error) {
	once := sync.Once{}
	var v T
	var err error
	return func() (T, error) {
		once.Do(func() { v, err = f() })
		return v, err
	}
}

// chdir holds the optional repository path provided via `-C/--chdir`.
// It is set early in main and consumed by the lazy detectors below.
var chdir string

// loadConfig lazily loads user configuration from ~/.config/gr/config.toml
var loadConfig = lazy(config.Load)

// detectLocal lazily resolves the local git repository (including worktree-aware
// paths and a go-git handle). It never performs network probing. This is used
// by commands that only need local repo state (e.g., local-only branch ops).
var detectLocal = lazy(func() (*provider.LocalRepo, error) {
	local, err := provider.FindLocalRepo(chdir)
	if err != nil {
		return nil, fmt.Errorf("repository detection: %w", err)
	}
	return local, nil
})

// detectProvider lazily resolves full provider/repo information based on the
// already-detected local repository. This may inspect remotes and probe the
// network for self-hosted instances. Used by PR commands and remote branch ops.
var detectProvider = lazy(func() (*provider.Info, error) {
	local, err := detectLocal()
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	info, err := provider.DetectFromRepo(local, cfg)
	if err != nil {
		return nil, fmt.Errorf("provider detection: %w", err)
	}
	if info.Provider == provider.ProviderUnknown {
		return nil, fmt.Errorf("cannot tell which service hosts %s (remote %s: %s); configure it in ~/.config/gr/config.toml:\n"+
			"  [hosts.%q]\n  provider = \"gitlab\"  # or \"github\", \"bitbucket\"", info.Host, info.Remote, info.URL, info.Host)
	}
	return info, nil
})

func main() {
	args := &Args{}
	p := arg.MustParse(args)
	chdir = args.Chdir
	// Ctrl-C (or SIGTERM) cancels in-flight API requests.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, args)
	interrupted := ctx.Err() != nil
	stop()
	if err != nil {
		if interrupted {
			err = &cliError{kind: kindInterrupted, msg: "interrupted"}
		}
		reportError(os.Stderr, err, wantsJSON(p.Subcommand()))
		os.Exit(exitCode(err))
	}
}

func run(ctx context.Context, args *Args) error {
	switch {
	case args.PR != nil:
		info, err := detectProvider()
		if err != nil {
			return err
		}
		return runPR(ctx, args.PR, info)
	case args.Branch != nil:
		return runBranch(ctx, args.Branch)
	default:
		return usageErrorf("no command provided. Try 'gr pr list' or 'gr --help'")
	}
}
