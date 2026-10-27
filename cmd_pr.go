package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/simonfxr/gr/pkg/provider"
	"github.com/simonfxr/gr/tables"
)

func runPR(ctx context.Context, cmd *PRCmd, info *provider.Info) error {
	if info == nil {
		return errors.New("cannot detect provider/repo info")
	}
	switch {
	case cmd.List != nil:
		return prList(ctx, info, cmd.List)
	case cmd.Create != nil:
		return prCreate(ctx, info, cmd.Create)
	case cmd.View != nil:
		return prView(ctx, info, cmd.View)
	case cmd.Browse != nil:
		return prBrowse(ctx, info, cmd.Browse)
	case cmd.Checkout != nil:
		return errors.New("pr checkout is not implemented yet")
	case cmd.Merge != nil:
		return prMerge(ctx, info, cmd.Merge)
	case cmd.Close != nil:
		return prClose(ctx, info, cmd.Close)
	case cmd.Todo != nil:
		return prTodo(ctx, info, cmd.Todo)
	case cmd.Comments != nil:
		return prComments(ctx, info, cmd.Comments)
	case cmd.AddComment != nil:
		return prAddComment(ctx, info, cmd.AddComment)
	case cmd.AddComments != nil:
		return prAddComments(ctx, info, cmd.AddComments)
	case cmd.Reply != nil:
		return prReply(ctx, info, cmd.Reply)
	case cmd.Resolve != nil:
		return prResolve(ctx, info, cmd.Resolve)
	case cmd.Tasks != nil:
		return prTasks(ctx, info, cmd.Tasks)
	case cmd.AddTask != nil:
		return prAddTask(ctx, info, cmd.AddTask)
	case cmd.ResolveTask != nil:
		return prResolveTask(ctx, info, cmd.ResolveTask)
	default:
		return usageErrorf("'gr pr' requires a subcommand. Try 'gr pr list'")
	}
}

func prList(ctx context.Context, info *provider.Info, cmd *PRListCmd) error {
	rows, err := info.Provider.PrList(ctx, info, provider.ListOptions{
		State:    cmd.State,
		Author:   cmd.Author,
		Assignee: cmd.Assignee,
		Base:     cmd.Base,
		Head:     cmd.Head,
		Limit:    cmd.Limit,
	})
	if err != nil {
		return err
	}
	if cmd.JSON {
		if rows == nil {
			rows = []provider.PullRequest{}
		}
		return printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Println("No pull requests found.")
		return nil
	}
	headers := []string{"ID", "Title", "Author", "State", "Created"}
	tables.Render(headers, func(yield func([]string) bool) {
		for _, pr := range rows {
			state := formatState(pr.State, pr.ProviderState, pr.Draft)
			if !yield([]string{fmt.Sprintf("#%d", pr.Number), pr.Title, pr.Author, state, formatTime(pr.CreatedAt)}) {
				return
			}
		}
	})
	return nil
}

func prView(ctx context.Context, info *provider.Info, cmd *PRViewCmd) error {
	number, err := resolvePR(ctx, info, cmd.PR, cmd.PRSelector)
	if err != nil {
		return err
	}
	d, err := info.Provider.PrView(ctx, info, number)
	if err != nil {
		return err
	}
	if cmd.JSON {
		return printJSON(d)
	}
	fmt.Printf("%s (#%d)\n", d.Title, d.Number)
	fmt.Printf("Author:   %s\n", d.Author)
	fmt.Printf("State:    %s\n", formatState(d.State, d.ProviderState, d.Draft))
	if d.Head != "" || d.Base != "" {
		fmt.Printf("Branch:   %s -> %s\n", d.Head, d.Base)
	}
	if s := formatTime(d.CreatedAt); s != "" {
		fmt.Printf("Created:  %s\n", s)
	}
	if s := formatTime(d.UpdatedAt); s != "" {
		fmt.Printf("Updated:  %s\n", s)
	}
	if d.URL != "" {
		fmt.Printf("URL:      %s\n", d.URL)
	}
	if strings.TrimSpace(d.Body) != "" {
		fmt.Println()
		fmt.Println(d.Body)
	}
	return nil
}

func prBrowse(ctx context.Context, info *provider.Info, cmd *PRBrowseCmd) error {
	number, err := resolvePR(ctx, info, cmd.PR, cmd.PRSelector)
	if err != nil {
		return err
	}
	d, err := info.Provider.PrView(ctx, info, number)
	if err != nil {
		return err
	}
	url := strings.TrimSpace(d.URL)
	if url == "" {
		return fmt.Errorf("PR #%d has no web URL", number)
	}
	if err := OpenBrowser(url); err != nil {
		return fmt.Errorf("opening browser: %w", err)
	}
	fmt.Printf("Opened %s\n", url)
	return nil
}

func prMerge(ctx context.Context, info *provider.Info, cmd *PRMergeCmd) error {
	opts := provider.MergeOptions{Method: cmd.Method}
	switch {
	case cmd.DeleteBranch && cmd.KeepBranch:
		return usageErrorf("--delete-branch and --keep-branch are mutually exclusive")
	case cmd.DeleteBranch || cmd.KeepBranch:
		opts.DeleteBranch = &cmd.DeleteBranch
	}
	number, err := resolvePR(ctx, info, cmd.PR, cmd.PRSelector)
	if err != nil {
		return err
	}
	res, err := info.Provider.PrMerge(ctx, info, number, opts)
	if res == nil {
		return err
	}
	// A non-nil result with an error means merged, but branch deletion failed.
	if cmd.JSON {
		if jerr := printJSON(res); jerr != nil {
			return jerr
		}
	} else if res.Merged {
		fmt.Printf("PR #%d merged.\n", number)
	} else {
		fmt.Printf("PR #%d is not merged yet (state: %s); the merge may be queued or still running.\n", number, res.State)
	}
	return err
}

func prClose(ctx context.Context, info *provider.Info, cmd *PRCloseCmd) error {
	number, err := resolvePR(ctx, info, cmd.PR, cmd.PRSelector)
	if err != nil {
		return err
	}
	res, err := info.Provider.PrClose(ctx, info, number, provider.CloseOptions{DeleteBranch: cmd.DeleteBranch})
	if res == nil {
		return err
	}
	// A non-nil result with an error means closed, but branch deletion failed.
	if cmd.JSON {
		if jerr := printJSON(res); jerr != nil {
			return jerr
		}
	} else {
		fmt.Printf("PR #%d %s.\n", number, formatState(res.State, res.ProviderState, false))
		if cmd.DeleteBranch && err == nil {
			fmt.Printf("Deleted source branch %q.\n", res.Head)
		}
	}
	return err
}

func prComments(ctx context.Context, info *provider.Info, cmd *PRCommentsCmd) error {
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	comments, err := info.Provider.PrComments(ctx, info, number)
	if err != nil {
		return err
	}
	if cmd.JSON {
		if comments == nil {
			comments = []provider.PRComment{}
		}
		return printJSON(comments)
	}
	if len(comments) == 0 {
		fmt.Printf("No comments for PR #%d.\n", number)
		return nil
	}
	headers := []string{"ID", "When", "Author", "Resolved", "Comment"}
	tables.Render(headers, func(yield func([]string) bool) {
		for _, c := range comments {
			body := c.Body
			if loc := formatLocation(c.Path, c.Line); loc != "" {
				body = fmt.Sprintf("[%s] %s", loc, body)
			}
			if c.ParentID > 0 {
				body = fmt.Sprintf("(reply to %d) %s", c.ParentID, body)
			}
			resolved := "-" // replies and comments that cannot be resolved
			if c.Resolvable {
				resolved = strconv.FormatBool(c.Resolved)
			}
			if !yield([]string{strconv.Itoa(c.ID), formatTime(c.CreatedAt), c.Author, resolved, body}) {
				return
			}
		}
	})
	return nil
}

func prAddComment(ctx context.Context, info *provider.Info, cmd *PRAddCommentCmd) error {
	filePath, line, err := parseFileLine(cmd.Location)
	if err != nil {
		return err
	}
	body, err := resolveBody(cmd.Body, cmd.BodyFile, "comment text")
	if err != nil {
		return err
	}
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	comment, err := info.Provider.PrAddComment(ctx, info, number, provider.AddCommentOptions{Path: filePath, Line: line, Body: body})
	if err != nil {
		return err
	}
	if cmd.JSON {
		return printJSON(comment)
	}
	fmt.Printf("Comment %d added to PR #%d at %s:%d\n", comment.ID, number, filePath, line)
	return nil
}

// addCommentResult is the per-item outcome of `pr addcomments`.
type addCommentResult struct {
	Path    string              `json:"path"`
	Line    int                 `json:"line"`
	OK      bool                `json:"ok"`
	Comment *provider.PRComment `json:"comment,omitempty"`
	Error   string              `json:"error,omitempty"`
}

func prAddComments(ctx context.Context, info *provider.Info, cmd *PRAddCommentsCmd) error {
	data, err := readInput(cmd.FromJSON)
	if err != nil {
		return fmt.Errorf("reading comments: %w", err)
	}
	var comments []provider.AddCommentOptions
	if err := json.Unmarshal(data, &comments); err != nil {
		return usageErrorf("parsing comments JSON: %v", err)
	}
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	results := make([]addCommentResult, 0, len(comments))
	failed := 0
	for _, c := range comments {
		r := addCommentResult{Path: c.Path, Line: c.Line}
		r.Comment, err = info.Provider.PrAddComment(ctx, info, number, c)
		if err != nil {
			r.Error = err.Error()
			failed++
		} else {
			r.OK = true
		}
		results = append(results, r)
		if !cmd.JSON {
			if r.OK {
				fmt.Printf("  added: %s:%d\n", c.Path, c.Line)
			} else {
				fmt.Fprintf(os.Stderr, "  failed: %s:%d: %s\n", c.Path, c.Line, r.Error)
			}
		}
	}
	if cmd.JSON {
		if err := printJSON(results); err != nil {
			return err
		}
	} else {
		fmt.Printf("Done: %d/%d comments added to PR #%d.\n", len(comments)-failed, len(comments), number)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d comments could not be added", failed, len(comments))
	}
	return nil
}

func prReply(ctx context.Context, info *provider.Info, cmd *PRReplyCmd) error {
	body, err := resolveBody(cmd.Body, cmd.BodyFile, "reply text")
	if err != nil {
		return err
	}
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	reply, err := info.Provider.PrReplyComment(ctx, info, number, cmd.CommentID, body)
	if err != nil {
		return err
	}
	if cmd.JSON {
		return printJSON(reply)
	}
	fmt.Printf("Reply %d added to comment %d on PR #%d\n", reply.ID, cmd.CommentID, number)
	return nil
}

func prResolve(ctx context.Context, info *provider.Info, cmd *PRResolveCmd) error {
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	if err := info.Provider.PrResolveComment(ctx, info, number, cmd.CommentID); err != nil {
		return err
	}
	if cmd.JSON {
		return printJSON(map[string]any{"pr": number, "comment_id": cmd.CommentID, "resolved": true})
	}
	fmt.Printf("Comment %d on PR #%d resolved.\n", cmd.CommentID, number)
	return nil
}

// prCreate handles PR creation, including optional editor editing.
func prCreate(ctx context.Context, info *provider.Info, create *PRCreateCmd) error {
	title := strings.TrimSpace(create.Title)
	body := create.Body
	if create.BodyFile != "" {
		if body != "" {
			return usageErrorf("pass the PR description either via --body or --body-file, not both")
		}
		b, err := readInput(create.BodyFile)
		if err != nil {
			return err
		}
		body = strings.TrimRight(string(b), " \t\r\n")
	}
	editMsgPath := ""
	if title == "" {
		if t, err := provider.LastCommitTitle(info); err == nil && strings.TrimSpace(t) != "" {
			title = t
		}
	}
	// If --edit is requested, open editor with PR_EDITMSG in gitdir
	if create.Edit {
		var err error
		editMsgPath = filepath.Join(cmp.Or(info.Worktree, info.GitDir), "PR_EDITMSG")
		if title, body, err = editPRMessage(editMsgPath, title, body); err != nil {
			return err
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
		return err
	}
	if editMsgPath != "" {
		_ = os.Remove(editMsgPath)
	}
	if create.JSON {
		return printJSON(res)
	}
	fmt.Printf("Created PR #%d: %s\n", res.Number, res.URL)
	return nil
}

// editPRMessage lets the user edit title and body in $VISUAL/$EDITOR. The
// first line is the title; the body starts after the first blank line.
func editPRMessage(msgPath, title, body string) (string, string, error) {
	initial := strings.TrimRight(title, "\n") + "\n\n" + strings.TrimRight(body, "\n") + "\n"
	if err := os.WriteFile(msgPath, []byte(initial), 0644); err != nil {
		return "", "", fmt.Errorf("cannot write %s: %w", msgPath, err)
	}
	editor := cmp.Or(strings.TrimSpace(os.Getenv("VISUAL")), strings.TrimSpace(os.Getenv("EDITOR")), "vi")
	cmdline := exec.Command(editor, msgPath)
	cmdline.Stdin = os.Stdin
	cmdline.Stdout = os.Stdout
	cmdline.Stderr = os.Stderr
	if err := cmdline.Run(); err != nil {
		return "", "", fmt.Errorf("editor failed: %w", err)
	}
	b, err := os.ReadFile(msgPath)
	if err != nil {
		return "", "", fmt.Errorf("cannot read %s: %w", msgPath, err)
	}
	content := strings.TrimSpace(string(b))
	if content == "" {
		return "", "", usageErrorf("aborted: empty PR message")
	}
	lines := strings.Split(content, "\n")
	title = strings.TrimSpace(lines[0])
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
	body = ""
	if bodyStart < len(lines) {
		body = strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))
	}
	if title == "" {
		return "", "", usageErrorf("aborted: empty PR title")
	}
	return title, body, nil
}

// parseFileLine parses "file:line" into path and line number.
func parseFileLine(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, usageErrorf("invalid location %q: expected file:line", s)
	}
	line, err := strconv.Atoi(s[i+1:])
	if err != nil || line <= 0 {
		return "", 0, usageErrorf("invalid line number in %q", s)
	}
	if s[:i] == "" {
		return "", 0, usageErrorf("invalid location %q: missing file", s)
	}
	return s[:i], line, nil
}

// formatState renders a normalized PR state, e.g. "closed (declined)" or
// "open, draft".
func formatState(state, providerState string, draft bool) string {
	// Only mention provider states that add information.
	switch providerState {
	case "", "open", "opened", "closed", "merged":
	default:
		state += " (" + providerState + ")"
	}
	if draft {
		state += ", draft"
	}
	return state
}

// formatLocation renders "path:line", "path" or "".
func formatLocation(path string, line int) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if line > 0 {
		return fmt.Sprintf("%s:%d", path, line)
	}
	return path
}
