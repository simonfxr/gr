package main

import (
	"fmt"
	"os"

	"github.com/alexflint/go-arg"

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
	List     *PRListCmd     `arg:"subcommand:list" help:"list pull requests"`
	Create   *PRCreateCmd   `arg:"subcommand:create" help:"create a pull request"`
	View     *PRViewCmd     `arg:"subcommand:view" help:"view a pull request"`
	Checkout *PRCheckoutCmd `arg:"subcommand:checkout" help:"checkout a pull request branch"`
	Merge    *PRMergeCmd    `arg:"subcommand:merge" help:"merge a pull request"`
	Close    *PRCloseCmd    `arg:"subcommand:close" help:"close a pull request"`
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
	Number int  `arg:"positional,required" help:"pull request number"`
	JSON   bool `arg:"--json" help:"output as JSON"`
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

func (Args) Description() string {
	return "gr - git remote PR helper (stubs)"
}

func main() {
	args := &Args{}
	arg.MustParse(args)

	// Detect provider once for subcommands; stay quiet in normal operation
	info, _ := provider.DetectFromRepo(args.Chdir)

	if args.PR != nil {
		runPR(args.PR, info)
		return
	}
	if args.Branch != nil {
		runBranch(args.Branch, info)
		return
	}

	// No subcommand provided: print error and exit 1
	fmt.Fprintln(os.Stderr, "Error: no command provided. Try 'gr pr list' or 'gr --help'.")
	os.Exit(1)
}
