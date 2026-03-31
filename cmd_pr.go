package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/simonfxr/gr/pkg/provider"
	"github.com/simonfxr/gr/tables"
)

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
		if cmd.List.JSON {
			b, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(b))
			return
		}
		if len(rows) == 0 {
			fmt.Println("No pull requests found.")
			return
		}
		headers := []string{"ID", "Title", "Author", "State", "Created"}
		tables.Render(headers, func(yield func([]string) bool) {
			for _, pr := range rows {
				created := ""
				if !pr.CreatedAt.IsZero() {
					created = pr.CreatedAt.Format(time.RFC3339)[:19]
				}
				if !yield([]string{fmt.Sprintf("#%d", pr.Number), pr.Title, pr.Author, pr.State, created}) {
					return
				}
			}
		})
	case cmd.Create != nil:
		handlePrCreate(info, cmd.Create)
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
		if cmd.View.JSON {
			b, _ := json.MarshalIndent(d, "", "  ")
			fmt.Println(string(b))
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
	case cmd.Browse != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		// If a number is provided, open that PR directly
		if cmd.Browse.Number > 0 {
			d, err := info.Provider.PrView(ctx, info, cmd.Browse.Number)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return
			}
			if strings.TrimSpace(d.URL) == "" {
				fmt.Fprintf(os.Stderr, "Error: PR #%d has no web URL\n", cmd.Browse.Number)
				return
			}
			if err := OpenBrowser(d.URL); err != nil {
				fmt.Fprintf(os.Stderr, "Error opening browser: %v\n", err)
				return
			}
			fmt.Printf("Opened %s\n", d.URL)
			return
		}
		// Otherwise, find PR by current branch
		branch, err := provider.CurrentBranch(info)
		if err != nil || strings.TrimSpace(branch) == "" {
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error determining current branch: %v\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "Error determining current branch")
			}
			return
		}
		rows, err := info.Provider.PrList(ctx, info, provider.ListOptions{State: "open", Head: branch, Limit: 5})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		if len(rows) == 0 {
			fmt.Printf("No open pull request found for branch %q.\n", branch)
			return
		}
		if len(rows) > 1 {
			fmt.Fprintf(os.Stderr, "Found %d PRs for branch %q. Please specify a PR number.\n", len(rows), branch)
			// Optionally show a short list
			for i, pr := range rows {
				if i >= 5 {
					break
				}
				fmt.Fprintf(os.Stderr, "  #%d %s (%s)\n", pr.Number, pr.Title, pr.URL)
			}
			return
		}
		// Exactly one PR found
		url := strings.TrimSpace(rows[0].URL)
		if url == "" {
			fmt.Fprintln(os.Stderr, "Error: PR has no web URL")
			return
		}
		if err := OpenBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "Error opening browser: %v\n", err)
			return
		}
		fmt.Printf("Opened %s\n", url)
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
		if cmd.Merge.JSON {
			b, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(b))
		} else {
			state := "merged"
			if res != nil && res.State != "" {
				state = res.State
			}
			fmt.Printf("PR #%d %s.\n", cmd.Merge.Number, state)
		}
	case cmd.Close != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		res, err := info.Provider.PrClose(ctx, info, cmd.Close.Number)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		// Optionally delete the source branch after close (best-effort)
		if cmd.Close.DeleteBranch && res != nil && strings.TrimSpace(res.Head) != "" {
			if err := info.Provider.BranchDelete(ctx, info, res.Head, provider.BranchDeleteOptions{Force: false}); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to delete branch %q: %v\n", res.Head, err)
			}
		}
		if cmd.Close.JSON {
			b, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(b))
		} else {
			state := "closed"
			if res != nil && res.State != "" {
				state = res.State
			}
			fmt.Printf("PR #%d %s.\n", cmd.Close.Number, state)
			if cmd.Close.DeleteBranch && res != nil && strings.TrimSpace(res.Head) != "" {
				fmt.Printf("Deleted remote branch %q.\n", res.Head)
			}
		}
	case cmd.Comments != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		// Resolve PR number either from --number or by branch
		prNumber := cmd.Comments.Number
		if prNumber <= 0 {
			branch := strings.TrimSpace(cmd.Comments.Branch)
			if branch == "" {
				b, err := provider.CurrentBranch(info)
				if err != nil || strings.TrimSpace(b) == "" {
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error determining current branch: %v\n", err)
					} else {
						fmt.Fprintln(os.Stderr, "Error determining current branch")
					}
					return
				}
				branch = b
			}
			rows, err := info.Provider.PrList(ctx, info, provider.ListOptions{State: "open", Head: branch, Limit: 5})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return
			}
			if len(rows) == 0 {
				fmt.Printf("No open pull request found for branch %q.\n", branch)
				return
			}
			if len(rows) > 1 {
				fmt.Fprintf(os.Stderr, "Found %d PRs for branch %q. Please specify --number.\n", len(rows), branch)
				for i, pr := range rows {
					if i >= 5 {
						break
					}
					fmt.Fprintf(os.Stderr, "  #%d %s (%s)\n", pr.Number, pr.Title, pr.URL)
				}
				return
			}
			prNumber = rows[0].Number
		}
		comments, err := info.Provider.PrComments(ctx, info, prNumber)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		if cmd.Comments.JSON {
			b, _ := json.MarshalIndent(comments, "", "  ")
			fmt.Println(string(b))
			return
		}
		if len(comments) == 0 {
			fmt.Printf("No comments for PR #%d.\n", prNumber)
			return
		}
		headers := []string{"ID", "When", "Author", "Comment"}
		tables.Render(headers, func(yield func([]string) bool) {
			for _, c := range comments {
				when := ""
				if !c.CreatedAt.IsZero() {
					when = c.CreatedAt.Format(time.RFC3339)[:19]
				}
				body := c.Body
				if path := strings.TrimSpace(c.Path); path != "" {
					// Prepend file context if present
					loc := path
					if c.Line > 0 {
						loc = fmt.Sprintf("%s:%d", path, c.Line)
					}
					body = fmt.Sprintf("[%s] %s", loc, body)
				}
				if !yield([]string{fmt.Sprintf("%d", c.ID), when, c.Author, body}) {
					return
				}
			}
		})
	case cmd.AddComment != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		// Parse file:line
		filePath, line, err := parseFileLine(cmd.AddComment.Location)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		// Resolve PR number
		prNumber := cmd.AddComment.Number
		if prNumber <= 0 {
			prNumber = resolvePRNumberFromBranch(ctx, info)
			if prNumber <= 0 {
				return
			}
		}
		err = info.Provider.PrAddComment(ctx, info, prNumber, provider.AddCommentOptions{
			Path: filePath,
			Line: line,
			Body: cmd.AddComment.Body,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		fmt.Printf("Comment added to PR #%d at %s:%d\n", prNumber, filePath, line)
	case cmd.Reply != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		prNumber := cmd.Reply.Number
		if prNumber <= 0 {
			prNumber = resolvePRNumberFromBranch(ctx, info)
			if prNumber <= 0 {
				return
			}
		}
		err := info.Provider.PrReplyComment(ctx, info, prNumber, cmd.Reply.CommentID, cmd.Reply.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		fmt.Printf("Reply added to comment %d on PR #%d\n", cmd.Reply.CommentID, prNumber)
	case cmd.Resolve != nil:
		if info == nil {
			fmt.Println("Cannot detect provider/repo info; aborting")
			return
		}
		ctx := context.Background()
		prNumber := cmd.Resolve.Number
		if prNumber <= 0 {
			prNumber = resolvePRNumberFromBranch(ctx, info)
			if prNumber <= 0 {
				return
			}
		}
		err := info.Provider.PrResolveComment(ctx, info, prNumber, cmd.Resolve.CommentID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		fmt.Printf("Comment %d on PR #%d resolved.\n", cmd.Resolve.CommentID, prNumber)
	default:
		fmt.Println("'gr pr' requires a subcommand. Try 'gr pr list'.")
	}
}

// handlePrCreate encapsulates the complex PR creation flow, including optional editor editing.
func handlePrCreate(info *provider.Info, create *PRCreateCmd) {
	if info == nil {
		fmt.Println("Cannot detect provider/repo info; aborting")
		return
	}
	ctx := context.Background()
	// Determine initial title/body defaults
	title := strings.TrimSpace(create.Title)
	body := create.Body
	editMsgPath := ""
	if title == "" {
		if t, err := provider.LastCommitTitle(info); err == nil && strings.TrimSpace(t) != "" {
			title = t
		}
	}
	// If --edit is requested, open editor with PR_EDITMSG in gitdir
	if create.Edit {
		editDir := cmp.Or(info.Worktree, info.GitDir)
		msgPath := filepath.Join(editDir, "PR_EDITMSG")
		initial := strings.TrimRight(title, "\n") + "\n\n" + strings.TrimRight(body, "\n") + "\n"
		if err := os.WriteFile(msgPath, []byte(initial), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot write %s: %v\n", msgPath, err)
			return
		}
		editMsgPath = msgPath
		editor := strings.TrimSpace(os.Getenv("VISUAL"))
		if editor == "" {
			editor = strings.TrimSpace(os.Getenv("EDITOR"))
		}
		if editor == "" {
			editor = "vi"
		}
		cmdline := exec.Command(editor, msgPath)
		cmdline.Stdin = os.Stdin
		cmdline.Stdout = os.Stdout
		cmdline.Stderr = os.Stderr
		if err := cmdline.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: editor failed: %v\n", err)
			return
		}
		// Read back content and parse
		b, err := os.ReadFile(msgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read %s: %v\n", msgPath, err)
			return
		}
		content := strings.TrimSpace(string(b))
		if content == "" {
			fmt.Println("Aborted: empty PR message.")
			return
		}
		lines := strings.Split(content, "\n")
		title = strings.TrimSpace(lines[0])
		// Separate body by first blank line
		bodyStart := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "" {
				bodyStart = i + 1
				break
			}
		}
		if bodyStart == -1 {
			bodyStart = 1
		}
		if bodyStart < len(lines) {
			body = strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))
		} else {
			body = ""
		}
		if title == "" {
			fmt.Println("Aborted: empty PR title.")
			return
		}
	}
	res, err := info.Provider.PrCreate(ctx, info, provider.CreateOptions{
		Title:            title,
		Body:             body,
		Base:             create.Base,
		Head:             create.Head,
		Draft:            create.Draft,
		SquashByDefault:  !create.NoSquash,
		DeleteAfterMerge: !create.NoDeleteAfterMerge,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	if create.Edit && editMsgPath != "" {
		_ = os.Remove(editMsgPath)
	}
	if create.JSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("Created PR #%d: %s\n", res.Number, res.URL)
	}
}

// parseFileLine parses "file:line" into path and line number.
func parseFileLine(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("invalid location %q: expected file:line", s)
	}
	line, err := strconv.Atoi(s[i+1:])
	if err != nil || line <= 0 {
		return "", 0, fmt.Errorf("invalid line number in %q", s)
	}
	return s[:i], line, nil
}

// resolvePRNumberFromBranch finds the open PR for the current branch. Returns 0 on failure.
func resolvePRNumberFromBranch(ctx context.Context, info *provider.Info) int {
	branch, err := provider.CurrentBranch(info)
	if err != nil || strings.TrimSpace(branch) == "" {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error determining current branch: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "Error determining current branch")
		}
		return 0
	}
	rows, err := info.Provider.PrList(ctx, info, provider.ListOptions{State: "open", Head: branch, Limit: 5})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "No open pull request found for branch %q.\n", branch)
		return 0
	}
	if len(rows) > 1 {
		fmt.Fprintf(os.Stderr, "Found %d PRs for branch %q. Please specify --number.\n", len(rows), branch)
		return 0
	}
	return rows[0].Number
}
