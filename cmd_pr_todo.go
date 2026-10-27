package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/simonfxr/gr/pkg/provider"
	"github.com/simonfxr/gr/tables"
)

// todoItem is one thing left to do on a PR: an open task or an unresolved,
// resolvable comment thread. Act on it, then close it with
// `gr pr resolvetask ID` (tasks) or `gr pr resolve ID` (comments).
type todoItem struct {
	Kind      string    `json:"kind"` // "task" or "comment"
	ID        int       `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Path      string    `json:"path,omitempty"`
	Line      int       `json:"line,omitempty"`
	URL       string    `json:"url,omitempty"`
	// Tasks only: the comment the task is attached to, and pending state.
	CommentID int  `json:"comment_id,omitempty"`
	Pending   bool `json:"pending,omitempty"`
	// Comments only: replies in the thread, oldest first.
	Replies []provider.PRComment `json:"replies,omitempty"`
}

type todoReport struct {
	PR                 int        `json:"pr"`
	OpenTasks          int        `json:"open_tasks"`
	UnresolvedComments int        `json:"unresolved_comments"`
	Items              []todoItem `json:"items"`
}

func prTodo(ctx context.Context, info *provider.Info, cmd *PRTodoCmd) error {
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	var (
		wg                  sync.WaitGroup
		comments            []provider.PRComment
		tasks               []provider.PRTask
		commentErr, taskErr error
	)
	wg.Go(func() { comments, commentErr = info.Provider.PrComments(ctx, info, number) })
	wg.Go(func() {
		tasks, taskErr = info.Provider.PrTasks(ctx, info, number, provider.TaskListOptions{State: "open"})
	})
	wg.Wait()
	if commentErr != nil {
		return commentErr
	}
	if taskErr != nil && !provider.IsUnsupported(taskErr) { // tasks are Bitbucket-only
		return taskErr
	}
	report := buildTodo(number, comments, tasks)
	if cmd.JSON {
		return printJSON(report)
	}
	if len(report.Items) == 0 {
		fmt.Printf("Nothing left on PR #%d: no open tasks or unresolved comments.\n", number)
		return nil
	}
	if tables.StdoutIsTerminal() {
		fmt.Printf("PR #%d: %d open tasks, %d unresolved comments\n\n", number, report.OpenTasks, report.UnresolvedComments)
	}
	headers := []string{"Kind", "ID", "Author", "Location", "Text"}
	tables.Render(headers, func(yield func([]string) bool) {
		for _, it := range report.Items {
			kind, text := it.Kind, it.Body
			if it.Pending {
				kind = "task (pending)"
			}
			if n := len(it.Replies); n > 0 {
				text = fmt.Sprintf("%s (%d replies)", text, n)
			}
			if !yield([]string{kind, strconv.Itoa(it.ID), it.Author, formatLocation(it.Path, it.Line), text}) {
				return
			}
		}
	})
	return nil
}

// buildTodo merges open tasks and unresolved comment threads into one
// chronological list. Resolution applies to whole threads, so replies are
// attached to their thread instead of being listed separately. Comments that
// cannot be resolved (e.g. plain GitLab notes) are not todo items.
func buildTodo(number int, comments []provider.PRComment, openTasks []provider.PRTask) todoReport {
	byID := make(map[int]provider.PRComment, len(comments))
	for _, c := range comments {
		byID[c.ID] = c
	}
	// root follows parent links to the top-level comment of a thread.
	root := func(c provider.PRComment) provider.PRComment {
		for seen := 0; c.ParentID > 0 && seen < len(comments); seen++ {
			p, ok := byID[c.ParentID]
			if !ok {
				break
			}
			c = p
		}
		return c
	}
	replies := make(map[int][]provider.PRComment)
	for _, c := range comments {
		if c.ParentID > 0 {
			r := root(c)
			replies[r.ID] = append(replies[r.ID], c)
		}
	}

	report := todoReport{PR: number, Items: []todoItem{}}
	for _, t := range openTasks {
		if t.Resolved {
			continue
		}
		it := todoItem{
			Kind: "task", ID: t.ID, Author: t.Creator, Body: t.Body, CreatedAt: t.CreatedAt,
			URL: t.URL, CommentID: t.CommentID, Pending: t.Pending,
		}
		if c, ok := byID[t.CommentID]; ok {
			if c.Path == "" {
				c = root(c)
			}
			it.Path, it.Line = c.Path, c.Line
			if it.URL == "" {
				it.URL = c.URL
			}
		}
		report.Items = append(report.Items, it)
		report.OpenTasks++
	}
	for _, c := range comments {
		if c.ParentID > 0 || c.Resolved || !c.Resolvable {
			continue
		}
		thread := replies[c.ID]
		slices.SortStableFunc(thread, func(a, b provider.PRComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
		report.Items = append(report.Items, todoItem{
			Kind: "comment", ID: c.ID, Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt,
			Path: c.Path, Line: c.Line, URL: c.URL, Replies: thread,
		})
		report.UnresolvedComments++
	}
	slices.SortStableFunc(report.Items, func(a, b todoItem) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return report
}
