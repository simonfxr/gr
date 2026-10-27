package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/simonfxr/gr/pkg/provider"
	"github.com/simonfxr/gr/tables"
)

func runBranch(ctx context.Context, cmd *BranchCmd) error {
	switch {
	case cmd.Rename != nil:
		return branchRename(ctx, cmd.Rename)
	case cmd.Delete != nil:
		return branchDelete(ctx, cmd.Delete)
	case cmd.List != nil:
		return branchList(ctx, cmd.List)
	case cmd.Browse != nil:
		return branchBrowse(cmd.Browse)
	default:
		return usageErrorf("'gr branch' requires a subcommand. Try 'gr branch list'")
	}
}

func branchRename(ctx context.Context, cmd *BranchRenameCmd) error {
	if cmd.LocalOnly {
		local, err := detectLocal()
		if err != nil {
			return err
		}
		if err := provider.LocalBranchRename(local.GitRepo, cmd.Old, cmd.New); err != nil {
			return err
		}
		fmt.Printf("Renamed local branch %q -> %q.\n", cmd.Old, cmd.New)
		return nil
	}
	info, err := detectProvider()
	if err != nil {
		return err
	}
	opts := provider.BranchRenameOptions{NoUpdatePRs: cmd.NoUpdatePRs, Force: cmd.Force}
	if err := info.Provider.BranchRename(ctx, info, cmd.Old, cmd.New, opts); err != nil {
		return err
	}
	fmt.Printf("Renamed remote branch %q -> %q.\n", cmd.Old, cmd.New)
	return nil
}

func branchDelete(ctx context.Context, d *BranchDeleteCmd) error {
	if d.LocalOnly && d.RemoteOnly {
		return usageErrorf("--local-only and --remote-only are mutually exclusive")
	}
	if d.LocalOnly {
		if d.DryRun {
			fmt.Printf("[dry-run] Would delete local branch %q.\n", d.Name)
			return nil
		}
		local, err := detectLocal()
		if err != nil {
			return err
		}
		if err := provider.LocalBranchDelete(local.GitRepo, d.Name, d.Force); err != nil {
			return err
		}
		fmt.Printf("Deleted local branch %q.\n", d.Name)
		return nil
	}
	info, err := detectProvider()
	if err != nil {
		return err
	}
	if d.DryRun {
		if d.RemoteOnly {
			fmt.Printf("[dry-run] Would delete remote branch %q on %s/%s.\n", d.Name, info.Owner, info.Repo)
		} else {
			fmt.Printf("[dry-run] Would delete remote branch %q on %s/%s and local branch.\n", d.Name, info.Owner, info.Repo)
		}
		return nil
	}
	if err := info.Provider.BranchDelete(ctx, info, d.Name, provider.BranchDeleteOptions{Force: d.Force}); err != nil {
		return err
	}
	if d.RemoteOnly {
		fmt.Printf("Deleted remote branch %q.\n", d.Name)
		return nil
	}
	if err := provider.LocalBranchDelete(info.GitRepo, d.Name, d.Force); err != nil {
		return fmt.Errorf("remote branch deleted, but failed to delete local branch: %w", err)
	}
	fmt.Printf("Deleted remote and local branch %q.\n", d.Name)
	return nil
}

func branchList(ctx context.Context, l *BranchListCmd) error {
	info, err := detectProvider()
	if err != nil {
		return err
	}
	rows, err := info.Provider.BranchListRemote(ctx, info, provider.BranchListOptions{Pattern: l.Pattern, Sort: l.Sort, Author: l.Author, Since: l.Since})
	if err != nil {
		return err
	}
	if l.JSON {
		if rows == nil {
			rows = []provider.BranchInfo{}
		}
		return printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Println("No branches found.")
		return nil
	}
	headers := []string{"Name", "Author", "Latest Commit"}
	tables.Render(headers, func(yield func([]string) bool) {
		for _, b := range rows {
			if !yield([]string{b.Name, b.Author, formatTime(b.CommitDate)}) {
				return
			}
		}
	})
	return nil
}

func branchBrowse(cmd *BranchBrowseCmd) error {
	info, err := detectProvider()
	if err != nil {
		return err
	}
	name := strings.TrimSpace(cmd.Name)
	if name == "" {
		if name, err = provider.CurrentBranch(info); err != nil {
			return usageErrorf("cannot determine current branch (%v); pass a branch name", err)
		}
	}
	url, err := info.Provider.BranchWebURL(info, name)
	if err != nil {
		return err
	}
	if err := OpenBrowser(url); err != nil {
		return fmt.Errorf("opening browser: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Opened %s\n", url)
	return nil
}

// formatTime renders t for tables (empty for the zero time).
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)[:19]
}
