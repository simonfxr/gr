package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		gitdir, err := provider.GitDirPath(info.Root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot resolve gitdir: %v\n", err)
			return
		}
		msgPath := filepath.Join(gitdir, "PR_EDITMSG")
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
