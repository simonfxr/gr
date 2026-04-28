package main

import (
	"fmt"
	"os"
	"sync"

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

// `gr pr` group and its subcommands (stubs for now)
type PRCmd struct {
	List       *PRListCmd       `arg:"subcommand:list" help:"list pull requests"`
	Create     *PRCreateCmd     `arg:"subcommand:create" help:"create a pull request"`
	View       *PRViewCmd       `arg:"subcommand:view" help:"view a pull request"`
	Browse     *PRBrowseCmd     `arg:"subcommand:browse" help:"open a pull request in the browser"`
	Checkout   *PRCheckoutCmd   `arg:"subcommand:checkout" help:"checkout a pull request branch"`
	Merge      *PRMergeCmd      `arg:"subcommand:merge" help:"merge a pull request"`
	Close      *PRCloseCmd      `arg:"subcommand:close" help:"close a pull request"`
	Comments   *PRCommentsCmd   `arg:"subcommand:comments" help:"show review comments for a pull/merge request"`
	AddComment  *PRAddCommentCmd  `arg:"subcommand:addcomment" help:"add an inline review comment to a pull request"`
	AddComments *PRAddCommentsCmd `arg:"subcommand:addcomments" help:"add multiple inline comments from a JSON file"`
	Reply       *PRReplyCmd       `arg:"subcommand:reply" help:"reply to a PR comment"`
	Resolve    *PRResolveCmd    `arg:"subcommand:resolve" help:"resolve a PR comment"`
}

type PRListCmd struct {
	State    string `arg:"--state" help:"filter by state: open, closed, merged, all" default:"open"`
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
	Number int  `arg:"positional" help:"pull request number (optional); if omitted, use current branch"`
	JSON   bool `arg:"--json" help:"output as JSON"`
}

type PRBrowseCmd struct {
	Number int `arg:"positional" help:"pull request number (optional); if omitted, use current branch"`
}

type PRCommentsCmd struct {
	Number int    `arg:"--number,-n" help:"pull request/merge request number"`
	Branch string `arg:"--branch,-b" help:"branch to resolve PR from when --number is not given (default: current branch)"`
	JSON   bool   `arg:"--json" help:"output as JSON"`
}

type PRAddCommentCmd struct {
	Number   int    `arg:"--number,-n" help:"pull request number (resolved from current branch if omitted)"`
	Location string `arg:"positional,required" help:"file:line location for the inline comment"`
	Body     string `arg:"positional,required" help:"comment text"`
	JSON     bool   `arg:"--json" help:"output as JSON"`
}

type PRAddCommentsCmd struct {
	Number   int    `arg:"--number,-n" help:"pull request number (resolved from current branch if omitted)"`
	FromJSON string `arg:"--from-json,required" help:"path to JSON file with comments array [{path, line, body}, ...]"`
}

type PRReplyCmd struct {
	Number    int    `arg:"--number,-n" help:"pull request number (resolved from current branch if omitted)"`
	CommentID int    `arg:"positional,required" help:"comment ID to reply to"`
	Body      string `arg:"positional,required" help:"reply text"`
}

type PRResolveCmd struct {
	Number    int `arg:"--number,-n" help:"pull request number (resolved from current branch if omitted)"`
	CommentID int `arg:"positional,required" help:"comment ID to resolve"`
}

type PRCheckoutCmd struct {
	Number int `arg:"positional,required" help:"pull request number"`
}

type PRMergeCmd struct {
	Number       int    `arg:"positional,required" help:"pull request number"`
	Method       string `arg:"--method" help:"merge method: merge|squash|rebase"`
	DeleteBranch bool   `arg:"--delete-branch" help:"delete source branch after merge"`
	JSON         bool   `arg:"--json" help:"output as JSON"`
}

type PRCloseCmd struct {
	Number       int  `arg:"positional,required" help:"pull request number"`
	DeleteBranch bool `arg:"--delete-branch" help:"delete source branch after closing"`
	JSON         bool `arg:"--json" help:"output as JSON"`
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
	return "gr - git remote PR helper (stubs)"
}

// lazy wraps a computation and ensures it runs at most once per process
// (thread-safe via sync.Once). Useful for deferring detection until needed.
func lazy[T any](f func() T) func() T {
	once := sync.Once{}
	var v T
	return func() T {
		once.Do(func() { v = f() })
		return v
	}
}

// chdir holds the optional repository path provided via `-C/--chdir`.
// It is set early in main and consumed by the lazy detectors below.
var chdir string

// loadConfig lazily loads user configuration from ~/.config/gr/config.toml
var loadConfig = lazy(func() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return cfg
})

// detectLocal lazily resolves the local git repository (including worktree-aware
// paths and a go-git handle). It never performs network probing. This is used
// by commands that only need local repo state (e.g., local-only branch ops).
var detectLocal = lazy(func() *provider.LocalRepo {
	local, err := provider.FindLocalRepo(chdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Repository detection error: %v\n", err)
		os.Exit(1)
	}
	return local
})

// detectProvider lazily resolves full provider/repo information based on the
// already-detected local repository. This may inspect remotes and probe the
// network for self-hosted instances. Used by PR commands and remote branch ops.
var detectProvider = lazy(func() *provider.Info {
	local := detectLocal()
	info, err := provider.DetectFromRepo(local, loadConfig())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Provider detection error: %v\n", err)
		os.Exit(1)
	}
	return info
})

func main() {
	args := &Args{}
	arg.MustParse(args)

	chdir = args.Chdir

	switch {
	case args.PR != nil:
		// PR commands require provider info
		runPR(args.PR, detectProvider())
	case args.Branch != nil:
		runBranch(args.Branch, detectLocal())
	default:
		fmt.Fprintln(os.Stderr, "Error: no command provided. Try 'gr pr list' or 'gr --help'.")
		os.Exit(1)
	}
}
