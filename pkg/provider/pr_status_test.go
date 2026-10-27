package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// jsonBody encodes v as a request body.
func jsonBody(t *testing.T, v any) io.ReadCloser {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return io.NopCloser(bytes.NewReader(b))
}

func TestGitHubPrStatus(t *testing.T) {
	f, info := fakeGitHub(t)
	pr := strings.Replace(githubPR7, `"draft":true`, `"draft":true,"mergeable":true,"mergeable_state":"blocked"`, 1)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, pr)
	var mutated bool
	threads := githubThreadsHandler(t, false, &mutated)
	f.handle("POST /api/graphql", func(w http.ResponseWriter, r *http.Request) {
		body := decodeJSON(t, r)
		query, _ := body["query"].(string)
		if !strings.Contains(query, "reviewDecision") {
			r.Body = jsonBody(t, body)
			threads(w, r)
			return
		}
		// bob approved and then commented; carol requested changes.
		writeJSON(w, http.StatusOK, `{"data":{"repository":{"pullRequest":{
			"reviewDecision":"CHANGES_REQUESTED",
			"latestReviews":{"nodes":[{"state":"COMMENTED","author":{"login":"bob"}},{"state":"CHANGES_REQUESTED","author":{"login":"carol"}}]},
			"latestOpinionatedReviews":{"nodes":[{"state":"APPROVED","author":{"login":"bob"}},{"state":"CHANGES_REQUESTED","author":{"login":"carol"}}]},
			"reviewRequests":{"nodes":[{"requestedReviewer":{"login":"dave"}},{"requestedReviewer":{"slug":"core","organization":{"login":"owner"}}}]}}}}}`)
	})
	f.reply("GET /api/v3/repos/owner/repo/commits/abc/check-runs", http.StatusOK,
		`{"check_runs":[{"name":"build","status":"completed","conclusion":"failure"}]}`)
	f.reply("GET /api/v3/repos/owner/repo/commits/abc/status", http.StatusOK, `{"statuses":[]}`)
	f.reply("GET /api/v3/repos/owner/repo/issues/7/comments", http.StatusOK, `[{"id":1,"body":"hi"}]`)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7/comments", http.StatusOK, `[{"id":100,"node_id":"C100","body":"fix","path":"a.go","line":1}]`)

	st, err := ProviderGitHub.PrStatus(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Warnings) > 0 {
		t.Fatalf("warnings: %v", st.Warnings)
	}
	wantReviews := &Reviews{Decision: DecisionChangesRequested, Approvals: 1, Reviewers: []Reviewer{
		{Login: "bob", State: ReviewApproved},
		{Login: "carol", State: ReviewChangesRequested},
		{Login: "dave", State: ReviewPending, Requested: true},
		{Login: "owner/core", State: ReviewPending, Requested: true},
	}}
	if !reflect.DeepEqual(st.Reviews, wantReviews) {
		t.Errorf("reviews = %+v\nwant %+v", st.Reviews, wantReviews)
	}
	if st.Checks == nil || st.Checks.State != CheckFail {
		t.Errorf("checks = %+v", st.Checks)
	}
	if *st.Comments != (CommentCounts{Total: 2, Unresolved: 1}) || st.Tasks != nil {
		t.Errorf("comments = %+v, tasks = %+v", st.Comments, st.Tasks)
	}
	// GitHub's "blocked" is explained by the other blockers.
	want := []string{BlockerDraft, BlockerChecksFailed, BlockerChangesRequested, BlockerUnresolvedComments}
	if m := st.Merge; m.Mergeable == nil || *m.Mergeable || m.ProviderStatus != "blocked" || !reflect.DeepEqual(m.Blockers, want) {
		t.Errorf("merge = %+v, want blockers %v", m, want)
	}
}

func TestGitHubMergeStatus(t *testing.T) {
	for _, tc := range []struct {
		state     string
		mergeable *bool
		blockers  []string
	}{
		{"clean", new(true), []string{}},
		{"unstable", new(true), []string{}},
		{"dirty", new(false), []string{BlockerConflicts}},
		{"behind", new(false), []string{BlockerBehind}},
		{"blocked", new(false), []string{BlockerBlocked}}, // no other reason known
		{"unknown", nil, []string{}},
	} {
		st := &PRStatus{PRDetails: PRDetails{State: StateOpen}}
		st.Merge = githubMergeStatus(&githublib.PullRequest{MergeableState: new(tc.state)})
		st.addBlockers()
		if !reflect.DeepEqual(st.Merge.Mergeable, tc.mergeable) || !reflect.DeepEqual(st.Merge.Blockers, tc.blockers) {
			t.Errorf("%s: merge = %+v", tc.state, st.Merge)
		}
	}
}

func TestGitLabPrStatus(t *testing.T) {
	f, info := fakeGitLab(t)
	mr := strings.Replace(gitlabMR5, `"state":"opened"`, `"state":"opened","detailed_merge_status":"not_approved",
		"head_pipeline":{"id":11,"status":"success"}`, 1)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5", http.StatusOK, mr)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/reviewers", http.StatusOK,
		`[{"user":{"username":"bob"},"state":"unreviewed"},{"user":{"username":"carol"},"state":"reviewed"}]`)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/approvals", http.StatusOK,
		`{"approved":false,"approvals_required":2,"approvals_left":1,"approved_by":[{"user":{"username":"alice"}}]}`)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/pipelines/11/jobs", http.StatusOK, `[{"name":"unit","stage":"test","status":"success"}]`)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/pipelines/11/bridges", http.StatusOK, `[]`)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/discussions", http.StatusOK, gitlabDiscussionsFixture)

	st, err := ProviderGitLab.PrStatus(t.Context(), info, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Warnings) > 0 {
		t.Fatalf("warnings: %v", st.Warnings)
	}
	// alice approved without being a reviewer.
	wantReviews := &Reviews{Decision: DecisionReviewRequired, Approvals: 1, ApprovalsRequired: 2, Reviewers: []Reviewer{
		{Login: "bob", State: ReviewPending, Requested: true},
		{Login: "carol", State: ReviewCommented, Requested: true},
		{Login: "alice", State: ReviewApproved},
	}}
	if !reflect.DeepEqual(st.Reviews, wantReviews) {
		t.Errorf("reviews = %+v\nwant %+v", st.Reviews, wantReviews)
	}
	if st.Checks == nil || st.Checks.State != CheckPass {
		t.Errorf("checks = %+v", st.Checks)
	}
	if st.Comments == nil || st.Comments.Total != 3 {
		t.Errorf("comments = %+v", st.Comments)
	}
	want := []string{BlockerDraft, BlockerApprovalRequired}
	if m := st.Merge; m.Mergeable == nil || *m.Mergeable || m.ProviderStatus != "not_approved" || !reflect.DeepEqual(m.Blockers, want) {
		t.Errorf("merge = %+v, want blockers %v", m, want)
	}
}

func TestGitLabMergeStatus(t *testing.T) {
	for _, tc := range []struct {
		status    string
		mergeable *bool
		blockers  []string
	}{
		{"mergeable", new(true), nil},
		{"checking", nil, nil},
		{"need_rebase", new(false), []string{BlockerBehind}},
		{"discussions_not_resolved", new(false), []string{BlockerUnresolvedComments}},
		{"merge_request_blocked", new(false), []string{BlockerBlocked}},
	} {
		m := gitlabMergeStatus(&gitlab.BasicMergeRequest{DetailedMergeStatus: tc.status})
		if !reflect.DeepEqual(m.Mergeable, tc.mergeable) || !reflect.DeepEqual(m.Blockers, tc.blockers) {
			t.Errorf("%s: merge = %+v", tc.status, m)
		}
	}
}

func TestBitbucketPrStatus(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	pr := strings.Replace(bitbucketPR7, `"draft":true`, `"draft":false,"participants":[
		{"user":{"nickname":"rev1"},"role":"REVIEWER","approved":true,"state":"approved"},
		{"user":{"nickname":"rev2"},"role":"REVIEWER","approved":false,"state":"changes_requested"},
		{"user":{"nickname":"rev3"},"role":"REVIEWER","approved":false,"state":null},
		{"user":{"nickname":"chatty"},"role":"PARTICIPANT","approved":false,"state":null}]`, 1)
	f.reply("GET /repositories/workspace/repo/pullrequests/7", http.StatusOK, pr)
	// The token may lack the permission to read build statuses.
	f.reply("GET /repositories/workspace/repo/pullrequests/7/statuses", http.StatusForbidden, `{"error":{"message":"forbidden"}}`)
	f.reply("GET /repositories/workspace/repo/pullrequests/7/comments", http.StatusOK,
		`{"values":[{"id":1,"content":{"raw":"open"}},{"id":2,"content":{"raw":"done"},"resolution":{"type":"comment_resolution"}}]}`)
	f.reply("GET /repositories/workspace/repo/pullrequests/7/tasks", http.StatusOK,
		`{"values":[{"id":1,"state":"UNRESOLVED","content":{"raw":"a"}},{"id":2,"state":"RESOLVED","content":{"raw":"b"}}]}`)

	st, err := ProviderBitbucket.PrStatus(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	if st.Checks != nil || len(st.Warnings) != 1 || !strings.HasPrefix(st.Warnings[0], "checks: ") {
		t.Errorf("checks = %+v, warnings = %q", st.Checks, st.Warnings)
	}
	wantReviews := &Reviews{Decision: DecisionChangesRequested, Approvals: 1, Reviewers: []Reviewer{
		{Login: "rev1", State: ReviewApproved, Requested: true},
		{Login: "rev2", State: ReviewChangesRequested, Requested: true},
		{Login: "rev3", State: ReviewPending, Requested: true},
		{Login: "chatty", State: ReviewCommented},
	}}
	if !reflect.DeepEqual(st.Reviews, wantReviews) {
		t.Errorf("reviews = %+v\nwant %+v", st.Reviews, wantReviews)
	}
	if *st.Comments != (CommentCounts{Total: 2, Unresolved: 1}) || st.Tasks == nil || *st.Tasks != (TaskCounts{Open: 1, Resolved: 1}) {
		t.Errorf("comments = %+v, tasks = %+v", st.Comments, st.Tasks)
	}
	want := []string{BlockerChangesRequested, BlockerUnresolvedComments, BlockerOpenTasks}
	if m := st.Merge; m.Mergeable != nil || !reflect.DeepEqual(m.Blockers, want) {
		t.Errorf("merge = %+v, want blockers %v", m, want)
	}
}

func TestPrStatusOfMergedPR(t *testing.T) {
	st := &PRStatus{PRDetails: PRDetails{State: StateMerged}, Comments: &CommentCounts{Unresolved: 2}}
	st.addBlockers()
	if !reflect.DeepEqual(st.Merge.Blockers, []string{BlockerNotOpen}) {
		t.Errorf("blockers = %v", st.Merge.Blockers)
	}
}
