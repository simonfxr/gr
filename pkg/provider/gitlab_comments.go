package provider

import (
	"cmp"
	"context"
	"errors"
	"strconv"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// GitLab organizes MR comments in discussions: a thread of notes, of which
// the first is the root. gr identifies comments by note ID; replying to or
// resolving a comment acts on the discussion that contains the note.

func prCommentsGitLab(ctx context.Context, nfo *Info, number int) ([]PRComment, error) {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	discussions, err := gitlabDiscussions(gl, nfo, number)
	if err != nil {
		return nil, err
	}
	out := []PRComment{}
	for _, d := range discussions {
		out = append(out, gitlabThread(nfo, number, d)...)
	}
	return out, nil
}

func prAddCommentGitLab(ctx context.Context, nfo *Info, number int, opts AddCommentOptions) (*PRComment, error) {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	mr, _, err := gl.MergeRequests.GetMergeRequest(gitlabProject(nfo), int64(number), nil)
	if err != nil {
		return nil, err
	}
	refs := mr.DiffRefs
	d, _, err := gl.Discussions.CreateMergeRequestDiscussion(gitlabProject(nfo), int64(number), &gitlab.CreateMergeRequestDiscussionOptions{
		Body: new(opts.Body),
		Position: &gitlab.PositionOptions{
			PositionType: new("text"),
			BaseSHA:      new(refs.BaseSha),
			StartSHA:     new(refs.StartSha),
			HeadSHA:      new(refs.HeadSha),
			OldPath:      new(opts.Path),
			NewPath:      new(opts.Path),
			NewLine:      new(int64(opts.Line)),
		},
	})
	if err != nil {
		return nil, err
	}
	return firstComment(gitlabThread(nfo, number, d))
}

func prReplyGitLab(ctx context.Context, nfo *Info, number, commentID int, body string) (*PRComment, error) {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	d, err := gitlabFindDiscussion(gl, nfo, number, commentID)
	if err != nil {
		return nil, err
	}
	// Replying to a standalone note turns it into a thread.
	note, _, err := gl.Discussions.AddMergeRequestDiscussionNote(gitlabProject(nfo), int64(number), d.ID,
		&gitlab.AddMergeRequestDiscussionNoteOptions{Body: new(body)})
	if err != nil {
		return nil, err
	}
	d.Notes = append(d.Notes, note)
	thread := gitlabThread(nfo, number, d)
	return &thread[len(thread)-1], nil
}

func prResolveGitLab(ctx context.Context, nfo *Info, number, commentID int) error {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return err
	}
	d, err := gitlabFindDiscussion(gl, nfo, number, commentID)
	if err != nil {
		return err
	}
	if thread := gitlabThread(nfo, number, d); len(thread) == 0 || !thread[0].Resolvable {
		return invalidArgf("comment %d on merge request !%d is not part of a resolvable thread", commentID, number)
	}
	_, _, err = gl.Discussions.ResolveMergeRequestDiscussion(gitlabProject(nfo), int64(number), d.ID,
		&gitlab.ResolveMergeRequestDiscussionOptions{Resolved: new(true)})
	return err
}

// gitlabDiscussions lists all discussions of a merge request.
func gitlabDiscussions(gl *gitlab.Client, nfo *Info, number int) ([]*gitlab.Discussion, error) {
	opts := &gitlab.ListMergeRequestDiscussionsOptions{ListOptions: gitlab.ListOptions{PerPage: 100, Page: 1}}
	var out []*gitlab.Discussion
	for {
		ds, resp, err := gl.Discussions.ListMergeRequestDiscussions(gitlabProject(nfo), int64(number), opts)
		if err != nil {
			return nil, err
		}
		out = append(out, ds...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// gitlabFindDiscussion returns the discussion that contains a note.
func gitlabFindDiscussion(gl *gitlab.Client, nfo *Info, number, noteID int) (*gitlab.Discussion, error) {
	discussions, err := gitlabDiscussions(gl, nfo, number)
	if err != nil {
		return nil, err
	}
	for _, d := range discussions {
		for _, n := range d.Notes {
			if n != nil && !n.System && n.ID == int64(noteID) {
				return d, nil
			}
		}
	}
	return nil, notFoundf("comment %d not found on merge request !%d", noteID, number)
}

// gitlabThread converts the notes of a discussion, skipping system notes.
// Later notes become replies to the first. A discussion is resolved when
// all its resolvable notes are; replies carry the thread's state.
func gitlabThread(nfo *Info, number int, d *gitlab.Discussion) []PRComment {
	resolvable, resolved := false, true
	for _, n := range d.Notes {
		if n != nil && !n.System && n.Resolvable {
			resolvable = true
			resolved = resolved && n.Resolved
		}
	}
	mrURL := gitlabMRWebURL(nfo, number)
	var out []PRComment
	for _, n := range d.Notes {
		if n == nil || n.System {
			continue
		}
		c := PRComment{
			ID:       int(n.ID),
			Author:   cmp.Or(strings.TrimSpace(n.Author.Username), strings.TrimSpace(n.Author.Name)),
			Body:     n.Body,
			Resolved: resolvable && resolved,
			URL:      mrURL + "#note_" + strconv.FormatInt(n.ID, 10),
		}
		if n.CreatedAt != nil {
			c.CreatedAt = *n.CreatedAt
		}
		if p := n.Position; p != nil {
			c.Path = cmp.Or(p.NewPath, p.OldPath)
			c.Line = int(cmp.Or(p.NewLine, p.OldLine))
		}
		if len(out) == 0 {
			c.Resolvable = resolvable
		} else {
			c.ParentID = out[0].ID
		}
		out = append(out, c)
	}
	return out
}

func gitlabMRWebURL(nfo *Info, number int) string {
	owner, repo := escapedRepoPath(nfo)
	return webRoot(ProviderGitLab, nfo) + "/" + owner + "/" + repo + "/-/merge_requests/" + strconv.Itoa(number)
}

func firstComment(thread []PRComment) (*PRComment, error) {
	if len(thread) == 0 {
		return nil, errors.New("gitlab returned a discussion without notes")
	}
	return &thread[0], nil
}
