package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/simonfxr/gr/pkg/provider"
	"github.com/simonfxr/gr/tables"
	"strings"
)

func runBranch(cmd *BranchCmd, local *provider.LocalRepo) {
	switch {
	case cmd.Rename != nil:
		if cmd.Rename.LocalOnly {
			if err := provider.LocalBranchRename(local.GitRepo, cmd.Rename.Old, cmd.Rename.New); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return
			}
			fmt.Printf("Renamed local branch %q -> %q.\n", cmd.Rename.Old, cmd.Rename.New)
			return
		}
		info := detectProvider()
		ctx := context.Background()
		// --force is an alias for skipping MR updates/safety checks on providers
		// where updating MR source branch is not supported (e.g., GitLab).
		if err := info.Provider.BranchRename(ctx, info, cmd.Rename.Old, cmd.Rename.New, provider.BranchRenameOptions{NoUpdatePRs: cmd.Rename.NoUpdatePRs || cmd.Rename.Force}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		fmt.Printf("Renamed remote branch %q -> %q.\n", cmd.Rename.Old, cmd.Rename.New)
	case cmd.Delete != nil:
		d := cmd.Delete
		if d.DryRun {
			// Describe what would happen
			if d.LocalOnly {
				fmt.Printf("[dry-run] Would delete local branch %q.\n", d.Name)
				return
			}
			info := detectProvider()
			if d.RemoteOnly {
				fmt.Printf("[dry-run] Would delete remote branch %q on %s/%s.\n", d.Name, info.Owner, info.Repo)
				return
			}
			fmt.Printf("[dry-run] Would delete remote branch %q on %s/%s and local branch.\n", d.Name, info.Owner, info.Repo)
			return
		}
		// Execute
		if d.LocalOnly {
			if err := provider.LocalBranchDelete(local.GitRepo, d.Name, d.Force); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return
			}
			fmt.Printf("Deleted local branch %q.\n", d.Name)
			return
		}
		ctx := context.Background()
		info := detectProvider()
		if d.RemoteOnly {
			if err := info.Provider.BranchDelete(ctx, info, d.Name, provider.BranchDeleteOptions{Force: d.Force}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return
			}
			fmt.Printf("Deleted remote branch %q.\n", d.Name)
			return
		}
		// Both: remote then local
		if err := info.Provider.BranchDelete(ctx, info, d.Name, provider.BranchDeleteOptions{Force: d.Force}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		if err := provider.LocalBranchDelete(info.GitRepo, d.Name, d.Force); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: remote deleted, but failed to delete local branch: %v\n", err)
			return
		}
		fmt.Printf("Deleted remote and local branch %q.\n", d.Name)
	case cmd.List != nil:
		l := cmd.List
		info := detectProvider()
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		rows, err := info.Provider.BranchListRemote(ctx, info, provider.BranchListOptions{Pattern: l.Pattern, Sort: l.Sort, Author: l.Author, Since: l.Since})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		if l.JSON {
			b, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(b))
			return
		}
		if len(rows) == 0 {
			fmt.Println("No branches found.")
			return
		}
		headers := []string{"Name", "Author", "Latest Commit"}
		tables.Render(headers, func(yield func([]string) bool) {
			for _, b := range rows {
				date := ""
				if !b.CommitDate.IsZero() {
					date = b.CommitDate.Format(time.RFC3339)[:19]
				}
				if !yield([]string{b.Name, b.Author, date}) {
					return
				}
			}
		})
	case cmd.Browse != nil:
		info := detectProvider()
		name := strings.TrimSpace(cmd.Browse.Name)
		if name == "" {
			if b, err := provider.CurrentBranch(info); err == nil {
				name = b
			} else {
				fmt.Fprintf(os.Stderr, "Error determining current branch: %v\n", err)
				return
			}
		}
		url, err := info.Provider.BranchWebURL(info, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		if err := OpenBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "Error opening browser: %v\n", err)
			return
		}
		fmt.Printf("Opened %s\n", url)
	default:
		fmt.Println("'gr branch' requires a subcommand. Try 'gr branch rename'.")
	}
}
