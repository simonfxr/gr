package provider

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const githubPR7 = `{"number":7,"title":"Add x","body":"desc","state":"open","draft":true,
	"user":{"login":"alice"},"created_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-02T10:00:00Z",
	"base":{"ref":"main","repo":{"name":"repo","owner":{"login":"owner"}}},
	"head":{"ref":"feature/x","sha":"abc","repo":{"name":"repo","owner":{"login":"owner"}}},
	"html_url":"https://github.example/owner/repo/pull/7"}`

func TestGitHubPrView(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, githubPR7)
	got, err := ProviderGitHub.PrView(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := &PRDetails{
		Number: 7, Title: "Add x", Body: "desc", Author: "alice", State: "open", ProviderState: "open",
		CreatedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC),
		Draft:     true, Base: "main", Head: "feature/x",
		URL: "https://github.example/owner/repo/pull/7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestGitHubPrViewNotFound(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/pulls/9", http.StatusNotFound, `{"message":"Not Found"}`)
	if _, err := ProviderGitHub.PrView(t.Context(), info, 9); !IsNotFound(err) {
		t.Errorf("err = %v, want not found", err)
	}
}

func TestGitHubPrListPaginatesAndFiltersAuthor(t *testing.T) {
	f, info := fakeGitHub(t)
	f.handle("GET /api/v3/repos/owner/repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "open" || q.Get("base") != "main" {
			t.Errorf("query = %v", q)
		}
		if q.Get("page") == "2" {
			writeJSON(w, http.StatusOK, `[{"number":3,"title":"c","state":"open","user":{"login":"Alice"}}]`)
			return
		}
		w.Header().Set("Link", `<http://`+r.Host+r.URL.Path+`?page=2>; rel="next"`)
		writeJSON(w, http.StatusOK, `[{"number":1,"title":"a","state":"open","user":{"login":"alice"}},
			{"number":2,"title":"b","state":"open","user":{"login":"bob"}}]`)
	})
	prs, err := ProviderGitHub.PrList(t.Context(), info, ListOptions{State: "open", Base: "main", Author: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var numbers []int
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
	}
	if !reflect.DeepEqual(numbers, []int{1, 3}) {
		t.Errorf("numbers = %v", numbers)
	}
}

func TestGitHubPrMergeDeletesBranch(t *testing.T) {
	for _, tc := range []struct {
		name, headRepo, deletePath string
	}{
		{"same repo", `{"name":"repo","owner":{"login":"owner"}}`, "/api/v3/repos/owner/repo/git/refs/heads/feature/x"},
		{"fork", `{"name":"repo-fork","owner":{"login":"alice"}}`, "/api/v3/repos/alice/repo-fork/git/refs/heads/feature/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, info := fakeGitHub(t)
			pr := strings.Replace(githubPR7, `"head":{"ref":"feature/x","sha":"abc","repo":{"name":"repo","owner":{"login":"owner"}}}`,
				`"head":{"ref":"feature/x","sha":"abc","repo":`+tc.headRepo+`}`, 1)
			f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, pr)
			f.reply("PUT /api/v3/repos/owner/repo/pulls/7/merge", http.StatusOK, `{"merged":true}`)
			f.reply("DELETE "+tc.deletePath, http.StatusNoContent, ``)
			if _, err := ProviderGitHub.PrMerge(t.Context(), info, 7, MergeOptions{DeleteBranch: new(true)}); err != nil {
				t.Fatal(err)
			}
			if !f.called("DELETE " + tc.deletePath) {
				t.Error("head branch not deleted")
			}
		})
	}
}

func TestGitHubPrMergeBranchDeletionErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		message string
		wantErr bool
	}{
		{"already deleted", http.StatusUnprocessableEntity, "Reference does not exist", false},
		{"forbidden", http.StatusForbidden, "Resource not accessible by integration", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, info := fakeGitHub(t)
			f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, githubPR7)
			f.reply("PUT /api/v3/repos/owner/repo/pulls/7/merge", http.StatusOK, `{"merged":true}`)
			f.reply("DELETE /api/v3/repos/owner/repo/git/refs/heads/feature/x", tc.status, `{"message":"`+tc.message+`"}`)
			d, err := ProviderGitHub.PrMerge(t.Context(), info, 7, MergeOptions{DeleteBranch: new(true)})
			if d == nil {
				t.Fatalf("merged PR not returned: %v", err)
			}
			if gotErr := err != nil && strings.Contains(err.Error(), "merged, but deleting branch"); gotErr != tc.wantErr {
				t.Errorf("err = %v, want error: %v", err, tc.wantErr)
			}
		})
	}
}

func TestGitHubPrMergeKeepsDefaultBranchOfFork(t *testing.T) {
	f, info := fakeGitHub(t)
	pr := strings.Replace(githubPR7, `"head":{"ref":"feature/x","sha":"abc","repo":{"name":"repo","owner":{"login":"owner"}}}`,
		`"head":{"ref":"main","sha":"abc","repo":{"name":"fork","owner":{"login":"alice"},"default_branch":"main"}}`, 1)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, pr)
	f.reply("PUT /api/v3/repos/owner/repo/pulls/7/merge", http.StatusOK, `{"merged":true}`)
	_, err := ProviderGitHub.PrMerge(t.Context(), info, 7, MergeOptions{DeleteBranch: new(true)})
	if err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Errorf("err = %v, want refusal to delete the default branch", err)
	}
}

// TestGitHubPrCloseDeletesForkBranch deletes the head branch in the fork,
// not a same-named branch of the base repository.
func TestGitHubPrCloseDeletesForkBranch(t *testing.T) {
	f, info := fakeGitHub(t)
	pr := strings.Replace(strings.Replace(githubPR7, `"state":"open"`, `"state":"closed"`, 1),
		`"head":{"ref":"feature/x","sha":"abc","repo":{"name":"repo","owner":{"login":"owner"}}}`,
		`"head":{"ref":"feature/x","sha":"abc","repo":{"name":"fork","owner":{"login":"alice"},"default_branch":"main"}}`, 1)
	f.reply("PATCH /api/v3/repos/owner/repo/pulls/7", http.StatusOK, pr)
	f.reply("DELETE /api/v3/repos/alice/fork/git/refs/heads/feature/x", http.StatusNoContent, ``)
	d, err := ProviderGitHub.PrClose(t.Context(), info, 7, CloseOptions{DeleteBranch: true})
	if err != nil || d.State != StateClosed {
		t.Fatalf("close = %+v, %v", d, err)
	}
	if !f.called("DELETE /api/v3/repos/alice/fork/git/refs/heads/feature/x") {
		t.Error("fork branch not deleted")
	}
}

func TestGitHubPrMergeKeepsBranchByDefault(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, githubPR7)
	f.reply("PUT /api/v3/repos/owner/repo/pulls/7/merge", http.StatusOK, `{"merged":true}`)
	if _, err := ProviderGitHub.PrMerge(t.Context(), info, 7, MergeOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubPrMergeMethod(t *testing.T) {
	for _, method := range []string{"", "merge", "Squash", "rebase"} {
		t.Run(method, func(t *testing.T) {
			f, info := fakeGitHub(t)
			f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, githubPR7)
			f.handle("PUT /api/v3/repos/owner/repo/pulls/7/merge", func(w http.ResponseWriter, r *http.Request) {
				got, _ := decodeJSON(t, r)["merge_method"].(string)
				if want := strings.ToLower(method); got != want {
					t.Errorf("merge_method = %q, want %q", got, want)
				}
				writeJSON(w, http.StatusOK, `{"merged":true}`)
			})
			if _, err := ProviderGitHub.PrMerge(t.Context(), info, 7, MergeOptions{Method: method}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPrMergeRejectsUnknownMethod(t *testing.T) {
	for _, p := range []Provider{ProviderGitHub, ProviderGitLab, ProviderBitbucket} {
		_, err := p.PrMerge(t.Context(), &Info{Provider: p}, 7, MergeOptions{Method: "fast-forward"})
		if _, ok := err.(*InvalidArgumentError); !ok {
			t.Errorf("%s: err = %v, want invalid argument", p, err)
		}
	}
}

func TestGitHubPrCreate(t *testing.T) {
	f, info := fakeGitHub(t)
	f.handle("GET /api/v3/repos/owner/repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("state") != "open" || q.Get("head") != "owner:feature/x" {
			t.Errorf("duplicate check query = %v", q)
		}
		writeJSON(w, http.StatusOK, `[]`)
	})
	f.handle("POST /api/v3/repos/owner/repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		want := map[string]any{"title": "Add x", "body": "desc", "head": "feature/x", "base": "main", "draft": true}
		if got := decodeJSON(t, r); !reflect.DeepEqual(got, want) {
			t.Errorf("payload = %v", got)
		}
		writeJSON(w, http.StatusCreated, githubPR7)
	})
	d, err := ProviderGitHub.PrCreate(t.Context(), info, CreateOptions{Title: "Add x", Body: "desc", Base: "main", Head: "feature/x", Draft: true})
	if err != nil || d.Number != 7 || !d.Draft {
		t.Errorf("got %+v, %v", d, err)
	}
}

func TestGitHubPrListStates(t *testing.T) {
	const page = `[{"number":1,"state":"open"},{"number":2,"state":"closed"},
		{"number":3,"state":"closed","merged_at":"2026-10-03T10:00:00Z"}]`
	for _, tc := range []struct {
		state, apiState string
		want            []int
	}{
		{"open", "open", []int{1}},
		{"closed", "closed", []int{2}},
		{"merged", "closed", []int{3}},
		{"all", "all", []int{1, 2, 3}},
	} {
		f, info := fakeGitHub(t)
		f.handle("GET /api/v3/repos/owner/repo/pulls", func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("state"); got != tc.apiState {
				t.Errorf("%s: API state = %q, want %q", tc.state, got, tc.apiState)
			}
			writeJSON(w, http.StatusOK, page)
		})
		prs, err := ProviderGitHub.PrList(t.Context(), info, ListOptions{State: tc.state})
		if err != nil {
			t.Fatal(err)
		}
		var numbers []int
		for _, pr := range prs {
			numbers = append(numbers, pr.Number)
			if pr.State != tc.state && tc.state != "all" {
				t.Errorf("%s: PR #%d has state %s", tc.state, pr.Number, pr.State)
			}
		}
		if !reflect.DeepEqual(numbers, tc.want) {
			t.Errorf("%s: numbers = %v, want %v", tc.state, numbers, tc.want)
		}
	}
}

// githubThreadsHandler serves the reviewThreads query (thread T1 whose root
// comment has node ID C100, resolved as given) and the resolveReviewThread
// mutation.
func githubThreadsHandler(t *testing.T, resolved bool, mutated *bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := decodeJSON(t, r)
		query, _ := req["query"].(string)
		vars, _ := req["variables"].(map[string]any)
		if strings.HasPrefix(query, "mutation") {
			if vars["id"] != "T1" {
				t.Errorf("mutation variables = %v", vars)
			}
			*mutated = true
			writeJSON(w, http.StatusOK, `{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`)
			return
		}
		if vars["owner"] != "owner" || vars["repo"] != "repo" || vars["number"] != float64(7) {
			t.Errorf("query variables = %v", vars)
		}
		writeJSON(w, http.StatusOK, `{"data":{"repository":{"pullRequest":{"reviewThreads":{
			"pageInfo":{"hasNextPage":false,"endCursor":"x"},
			"nodes":[{"id":"T1","isResolved":`+strconv.FormatBool(resolved)+`,
				"comments":{"nodes":[{"id":"C100"}]}}]}}}}}`)
	}
}

func TestGitHubPrComments(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/issues/7/comments", http.StatusOK,
		`[{"id":1,"body":"LGTM?","user":{"login":"bob"},"created_at":"2026-10-01T09:00:00Z","html_url":"u1"}]`)
	// Comment IDs exceed 32 bits; threads are matched by node ID.
	f.reply("GET /api/v3/repos/owner/repo/pulls/7/comments", http.StatusOK, `[
		{"id":100,"node_id":"C100","body":"rename","user":{"login":"rev"},"path":"a.go","line":3,"created_at":"2026-10-01T10:00:00Z","html_url":"u100"},
		{"id":4294967396,"node_id":"C4294967396","body":"done","user":{"login":"alice"},"path":"a.go","original_line":3,"in_reply_to_id":100,"created_at":"2026-10-01T11:00:00Z"},
		{"id":4294967397,"node_id":"C4294967397","body":"no thread","user":{"login":"rev"},"path":"b.go","line":1,"created_at":"2026-10-01T12:00:00Z"}]`)
	var mutated bool
	f.handle("POST /api/graphql", githubThreadsHandler(t, false, &mutated))
	comments, err := ProviderGitHub.PrComments(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	at := func(h int) time.Time { return time.Date(2026, 10, 1, h, 0, 0, 0, time.UTC) }
	want := []PRComment{
		{ID: 1, Author: "bob", Body: "LGTM?", CreatedAt: at(9), URL: "u1"},
		{ID: 100, Author: "rev", Body: "rename", CreatedAt: at(10), Resolvable: true, Path: "a.go", Line: 3, URL: "u100"},
		{ID: 4294967396, Author: "alice", Body: "done", CreatedAt: at(11), Path: "a.go", Line: 3, ParentID: 100},
		{ID: 4294967397, Author: "rev", Body: "no thread", CreatedAt: at(12), Path: "b.go", Line: 1},
	}
	if !reflect.DeepEqual(comments, want) {
		t.Errorf("comments:\n got %+v\nwant %+v", comments, want)
	}
}

func TestGitHubPrAddComment(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, githubPR7)
	f.handle("POST /api/v3/repos/owner/repo/pulls/7/comments", func(w http.ResponseWriter, r *http.Request) {
		want := map[string]any{"body": "fix", "commit_id": "abc", "path": "a.go", "line": float64(9), "side": "RIGHT"}
		if got := decodeJSON(t, r); !reflect.DeepEqual(got, want) {
			t.Errorf("payload = %v", got)
		}
		writeJSON(w, http.StatusCreated, `{"id":200,"body":"fix","path":"a.go","line":9}`)
	})
	c, err := ProviderGitHub.PrAddComment(t.Context(), info, 7, AddCommentOptions{Path: "a.go", Line: 9, Body: "fix"})
	if err != nil || c.ID != 200 || !c.Resolvable || c.Line != 9 {
		t.Errorf("got %+v, %v", c, err)
	}
}

func TestGitHubPrReply(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/101", http.StatusOK, `{"id":101,"in_reply_to_id":100}`)
	f.handle("POST /api/v3/repos/owner/repo/pulls/7/comments", func(w http.ResponseWriter, r *http.Request) {
		if got := decodeJSON(t, r); !reflect.DeepEqual(got, map[string]any{"body": "ok", "in_reply_to": float64(100)}) {
			t.Errorf("payload = %v", got)
		}
		writeJSON(w, http.StatusCreated, `{"id":102,"body":"ok","in_reply_to_id":100}`)
	})
	if c, err := ProviderGitHub.PrReplyComment(t.Context(), info, 7, 101, "ok"); err != nil || c.ID != 102 || c.ParentID != 100 {
		t.Errorf("review reply = %+v, %v", c, err)
	}

	// Conversation comments have no threads: quote reply.
	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/1", http.StatusNotFound, `{"message":"Not Found"}`)
	f.reply("GET /api/v3/repos/owner/repo/issues/comments/1", http.StatusOK, `{"id":1,"body":"LGTM?\nreally","user":{"login":"bob"}}`)
	f.handle("POST /api/v3/repos/owner/repo/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		if got := decodeJSON(t, r)["body"]; got != "> @bob wrote:\n>\n> LGTM?\n> really\n\nyes" {
			t.Errorf("body = %q", got)
		}
		writeJSON(w, http.StatusCreated, `{"id":2,"body":"..."}`)
	})
	if c, err := ProviderGitHub.PrReplyComment(t.Context(), info, 7, 1, "yes"); err != nil || c.ID != 2 {
		t.Errorf("quote reply = %+v, %v", c, err)
	}

	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/9", http.StatusNotFound, `{"message":"Not Found"}`)
	f.reply("GET /api/v3/repos/owner/repo/issues/comments/9", http.StatusNotFound, `{"message":"Not Found"}`)
	if _, err := ProviderGitHub.PrReplyComment(t.Context(), info, 7, 9, "x"); !IsNotFound(err) {
		t.Errorf("unknown comment: err = %v", err)
	}

	// A conversation comment of another issue is not quoted into this PR.
	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/5", http.StatusNotFound, `{"message":"Not Found"}`)
	f.reply("GET /api/v3/repos/owner/repo/issues/comments/5", http.StatusOK,
		`{"id":5,"body":"x","issue_url":"https://github.example/api/v3/repos/owner/repo/issues/70"}`)
	if _, err := ProviderGitHub.PrReplyComment(t.Context(), info, 7, 5, "x"); !IsNotFound(err) {
		t.Errorf("comment of another issue: err = %v", err)
	}
}

func TestGitHubPrResolve(t *testing.T) {
	f, info := fakeGitHub(t)
	var mutated bool
	f.handle("POST /api/graphql", githubThreadsHandler(t, false, &mutated))
	const prURL = `"pull_request_url":"https://github.example/api/v3/repos/owner/repo/pulls/7"`
	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/100", http.StatusOK, `{"id":100,"node_id":"C100",`+prURL+`}`)
	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/101", http.StatusOK, `{"id":101,"node_id":"C101","in_reply_to_id":100,`+prURL+`}`)
	// A reply's ID resolves its thread.
	if err := ProviderGitHub.PrResolveComment(t.Context(), info, 7, 101); err != nil || !mutated {
		t.Errorf("resolve: %v (mutated %v)", err, mutated)
	}

	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/1", http.StatusNotFound, `{"message":"Not Found"}`)
	f.reply("GET /api/v3/repos/owner/repo/issues/comments/1", http.StatusOK,
		`{"id":1,"issue_url":"https://github.example/api/v3/repos/owner/repo/issues/7"}`)
	if err := ProviderGitHub.PrResolveComment(t.Context(), info, 7, 1); !isInvalidArg(err) {
		t.Errorf("conversation comment: err = %v", err)
	}

	// A review comment of another PR is not found on this one.
	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/300", http.StatusOK,
		`{"id":300,"node_id":"C300","pull_request_url":"https://github.example/api/v3/repos/owner/repo/pulls/8"}`)
	if err := ProviderGitHub.PrResolveComment(t.Context(), info, 7, 300); !IsNotFound(err) {
		t.Errorf("comment of another PR: err = %v", err)
	}

	// Errors other than 404 are reported as they are.
	f.reply("GET /api/v3/repos/owner/repo/pulls/comments/2", http.StatusNotFound, `{"message":"Not Found"}`)
	f.reply("GET /api/v3/repos/owner/repo/issues/comments/2", http.StatusInternalServerError, `{"message":"boom"}`)
	if err := ProviderGitHub.PrResolveComment(t.Context(), info, 7, 2); err == nil || IsNotFound(err) {
		t.Errorf("server error: err = %v, want it reported", err)
	}
}

func TestGitHubBranchList(t *testing.T) {
	f, info := fakeGitHub(t)
	f.handle("POST /api/graphql", func(w http.ResponseWriter, r *http.Request) {
		vars, _ := decodeJSON(t, r)["variables"].(map[string]any)
		if vars["cursor"] == "c1" {
			writeJSON(w, http.StatusOK, `{"data":{"repository":{"refs":{"pageInfo":{"hasNextPage":false},
				"nodes":[{"name":"feature/b","target":{"author":{"name":"Bob","date":"2026-10-03T10:00:00Z"}}}]}}}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"repository":{"refs":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},
			"nodes":[{"name":"main","target":{"author":{"name":"Alice","date":"2026-10-01T10:00:00Z"}}},
				{"name":"tag-like","target":{}}]}}}}`)
	})
	branches, err := ProviderGitHub.BranchListRemote(t.Context(), info, BranchListOptions{Sort: "date"})
	if err != nil {
		t.Fatal(err)
	}
	want := []BranchInfo{
		{Name: "feature/b", Author: "Bob", CommitDate: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)},
		{Name: "main", Author: "Alice", CommitDate: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)},
		{Name: "tag-like"},
	}
	if !reflect.DeepEqual(branches, want) {
		t.Errorf("branches = %+v", branches)
	}
}
