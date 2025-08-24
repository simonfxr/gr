package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alexflint/go-arg"

	"github.com/simonfxr/gr/pkg/provider"
)

// Top-level CLI
type Args struct {
	Chdir string `arg:"-C,--chdir" help:"path to repo (like git -C DIR)"`
	PR    *PRCmd `arg:"subcommand:pr" help:"pull request commands"`
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
}

type PRCreateCmd struct {
	Title              string `arg:"--title" help:"PR title"`
	Body               string `arg:"--body" help:"PR description/body"`
	Base               string `arg:"--base" help:"target branch (default: repo default)"`
	Head               string `arg:"--head" help:"source branch (default: current)"`
	Draft              bool   `arg:"--draft" help:"create as draft PR"`
	NoEdit             bool   `arg:"--no-edit" help:"skip interactive editing"`
	NoDeleteAfterMerge bool   `arg:"--no-delete-after-merge" help:"keep source branch after merge (default: delete)"`
}

type PRViewCmd struct {
	Number int `arg:"positional,required" help:"pull request number"`
}

type PRCheckoutCmd struct {
	Number int `arg:"positional,required" help:"pull request number"`
}

type PRMergeCmd struct {
	Number       int    `arg:"positional,required" help:"pull request number"`
	Method       string `arg:"--method" help:"merge method: merge|squash|rebase"`
	DeleteBranch bool   `arg:"--delete-branch" help:"delete source branch after merge"`
}

type PRCloseCmd struct {
	Number int `arg:"positional,required" help:"pull request number"`
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

	// No subcommand provided: print error and exit 1
	fmt.Fprintln(os.Stderr, "Error: no command provided. Try 'gr pr list' or 'gr --help'.")
	os.Exit(1)
}

func runPR(cmd *PRCmd, info *provider.Info) {
	switch {
	case cmd.List != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		opts := provider.ListOptions{
			State:    cmd.List.State,
			Author:   cmd.List.Author,
			Assignee: cmd.List.Assignee,
			Base:     cmd.List.Base,
			Head:     cmd.List.Head,
			Limit:    cmd.List.Limit,
		}
		rows, err := info.Provider.PrList(ctx, info, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		if len(rows) == 0 {
			fmt.Println("No pull requests found.")
			return
		}
		// Print a simple table
		fmt.Printf("%-6s  %-60s  %-12s  %-8s  %-20s\n", "ID", "Title", "Author", "State", "Created")
		for _, pr := range rows {
			title := pr.Title
			if len(title) > 60 {
				title = title[:57] + "..."
			}
			fmt.Printf("#%-5d  %-60s  %-12s  %-8s  %-20s\n",
				pr.Number, title, pr.Author, pr.State, pr.CreatedAt.Format(time.RFC3339)[:19])
		}
	case cmd.Create != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		res, err := info.Provider.PrCreate(ctx, info, provider.CreateOptions{
			Title:            cmd.Create.Title,
			Body:             cmd.Create.Body,
			Base:             cmd.Create.Base,
			Head:             cmd.Create.Head,
			Draft:            cmd.Create.Draft,
			DeleteAfterMerge: !cmd.Create.NoDeleteAfterMerge,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		fmt.Printf("Created PR #%d: %s\n", res.Number, res.URL)
	case cmd.View != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		d, err := info.Provider.PrView(ctx, info, cmd.View.Number)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		// Print details
		fmt.Printf("%s (#%d)\n", d.Title, d.Number)
		fmt.Printf("Author:   %s\n", d.Author)
		state := d.State
		if d.Merged {
			state = "merged"
		}
		fmt.Printf("State:    %s\n", state)
		if d.Draft {
			fmt.Printf("Draft:    %v\n", d.Draft)
		}
		if d.Head != "" || d.Base != "" {
			fmt.Printf("Branch:   %s -> %s\n", d.Head, d.Base)
		}
		if !d.CreatedAt.IsZero() {
			fmt.Printf("Created:  %s\n", d.CreatedAt.Format(time.RFC3339)[:19])
		}
		if !d.UpdatedAt.IsZero() {
			fmt.Printf("Updated:  %s\n", d.UpdatedAt.Format(time.RFC3339)[:19])
		}
		if d.URL != "" {
			fmt.Printf("URL:      %s\n", d.URL)
		}
		if strings.TrimSpace(d.Body) != "" {
			fmt.Println()
			fmt.Println(d.Body)
		}
	case cmd.Checkout != nil:
		fmt.Printf("[stub] pr checkout #%d\n", cmd.Checkout.Number)
	case cmd.Merge != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		res, err := info.Provider.PrMerge(ctx, info, cmd.Merge.Number, provider.MergeOptions{
			Method:       cmd.Merge.Method,
			DeleteBranch: cmd.Merge.DeleteBranch,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		state := "merged"
		if res != nil && res.State != "" {
			state = res.State
		}
		fmt.Printf("PR #%d %s.\n", cmd.Merge.Number, state)
	case cmd.Close != nil:
		fmt.Printf("[stub] pr close #%d\n", cmd.Close.Number)
	default:
		fmt.Println("'gr pr' requires a subcommand. Try 'gr pr list'.")
	}
}
