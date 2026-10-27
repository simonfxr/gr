package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PRTask is a pull request task (Bitbucket's checklist items shown on a PR).
type PRTask struct {
	ID         int       `json:"id"`
	Body       string    `json:"body"`
	Resolved   bool      `json:"resolved"`
	Pending    bool      `json:"pending"`
	Creator    string    `json:"creator"`
	CreatedAt  time.Time `json:"created_at"`
	ResolvedBy string    `json:"resolved_by,omitempty"`
	ResolvedAt time.Time `json:"resolved_at,omitzero"`
	// CommentID is set when the task is attached to a PR comment.
	CommentID int    `json:"comment_id,omitempty"`
	URL       string `json:"url,omitempty"`
}

// TaskListOptions filters PR tasks. State is one of open, resolved or all.
type TaskListOptions struct {
	State string
}

// AddTaskOptions holds parameters for creating a PR task.
type AddTaskOptions struct {
	Body string `json:"body"`
	// CommentID attaches the task to an existing comment (optional).
	CommentID int `json:"comment_id,omitempty"`
	// Pending creates the task as part of the caller's unsubmitted review.
	Pending bool `json:"pending,omitempty"`
}

// PrTasks lists tasks of a PR.
func (p Provider) PrTasks(ctx context.Context, nfo *Info, number int, opts TaskListOptions) ([]PRTask, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderBitbucket:
		return prTasksBitbucket(ctx, nfo, number, opts)
	default:
		return nil, unsupported("PR tasks (a Bitbucket feature)", p)
	}
}

// PrAddTask creates a task on a PR.
func (p Provider) PrAddTask(ctx context.Context, nfo *Info, number int, opts AddTaskOptions) (*PRTask, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	if strings.TrimSpace(opts.Body) == "" {
		return nil, invalidArgf("task body must not be empty")
	}
	switch p {
	case ProviderBitbucket:
		return prAddTaskBitbucket(ctx, nfo, number, opts)
	default:
		return nil, unsupported("PR tasks (a Bitbucket feature)", p)
	}
}

// PrSetTaskResolved resolves (or reopens) a PR task.
func (p Provider) PrSetTaskResolved(ctx context.Context, nfo *Info, number, taskID int, resolved bool) (*PRTask, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderBitbucket:
		return prSetTaskResolvedBitbucket(ctx, nfo, number, taskID, resolved)
	default:
		return nil, unsupported("PR tasks (a Bitbucket feature)", p)
	}
}

type bitbucketTask struct {
	ID      int `json:"id"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	State      string         `json:"state"`
	Pending    bool           `json:"pending"`
	CreatedOn  string         `json:"created_on"`
	Creator    *bitbucketUser `json:"creator"`
	ResolvedOn string         `json:"resolved_on"`
	ResolvedBy *bitbucketUser `json:"resolved_by"`
	Comment    *struct {
		ID int `json:"id"`
	} `json:"comment"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func (t *bitbucketTask) toPRTask() PRTask {
	out := PRTask{
		ID:         t.ID,
		Body:       t.Content.Raw,
		Resolved:   strings.EqualFold(t.State, "RESOLVED"),
		Pending:    t.Pending,
		Creator:    t.Creator.name(),
		ResolvedBy: t.ResolvedBy.name(),
		URL:        t.Links.HTML.Href,
	}
	out.CreatedAt, _ = time.Parse(time.RFC3339, t.CreatedOn)
	if out.Resolved {
		out.ResolvedAt, _ = time.Parse(time.RFC3339, t.ResolvedOn)
	} else {
		out.ResolvedBy = ""
	}
	if t.Comment != nil {
		out.CommentID = t.Comment.ID
	}
	return out
}

func bitbucketTasksEndpoint(nfo *Info, number int) string {
	return bitbucketPREndpoint(nfo, number) + "/tasks"
}

func prTasksBitbucket(ctx context.Context, nfo *Info, number int, opts TaskListOptions) ([]PRTask, error) {
	params := url.Values{}
	switch strings.ToLower(strings.TrimSpace(opts.State)) {
	case "", "all":
	case "open", "unresolved":
		params.Set("q", `state = "UNRESOLVED"`)
	case "resolved", "done":
		params.Set("q", `state = "RESOLVED"`)
	default:
		return nil, invalidArgf("invalid task state %q (want open, resolved or all)", opts.State)
	}
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	out := []PRTask{}
	err = bitbucketListPages(ctx, bb, bitbucketTasksEndpoint(nfo, number), params, func(t bitbucketTask) bool {
		out = append(out, t.toPRTask())
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func prAddTaskBitbucket(ctx context.Context, nfo *Info, number int, opts AddTaskOptions) (*PRTask, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"content": map[string]any{"raw": opts.Body},
	}
	if opts.CommentID > 0 {
		payload["comment"] = map[string]any{"id": opts.CommentID}
	}
	if opts.Pending {
		payload["pending"] = true
	}
	u, err := bb.endpointURL(bitbucketTasksEndpoint(nfo, number))
	if err != nil {
		return nil, err
	}
	var res bitbucketTask
	if err := bb.do(ctx, http.MethodPost, u, payload, &res); err != nil {
		return nil, err
	}
	t := res.toPRTask()
	return &t, nil
}

func prSetTaskResolvedBitbucket(ctx context.Context, nfo *Info, number, taskID int, resolved bool) (*PRTask, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	state := "UNRESOLVED"
	if resolved {
		state = "RESOLVED"
	}
	u, err := bb.endpointURL(bitbucketTasksEndpoint(nfo, number) + "/" + strconv.Itoa(taskID))
	if err != nil {
		return nil, err
	}
	var res bitbucketTask
	if err := bb.do(ctx, http.MethodPut, u, map[string]any{"state": state}, &res); err != nil {
		return nil, err
	}
	t := res.toPRTask()
	return &t, nil
}
