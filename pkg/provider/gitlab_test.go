package provider

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const gitlabMR5 = `{"iid":5,"title":"Add y","description":"desc","state":"opened","draft":true,
	"author":{"username":"bob"},"created_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-02T10:00:00Z",
	"source_branch":"feature/y","target_branch":"main",
	"web_url":"https://gitlab.example/group/sub/proj/-/merge_requests/5"}`

func TestGitLabPrView(t *testing.T) {
	f, info := fakeGitLab(t)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5", http.StatusOK, gitlabMR5)
	got, err := ProviderGitLab.PrView(t.Context(), info, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 5 || got.Title != "Add y" || got.Body != "desc" || got.Author != "bob" ||
		got.Base != "main" || got.Head != "feature/y" || got.Merged ||
		!got.CreatedAt.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) ||
		got.URL != "https://gitlab.example/group/sub/proj/-/merge_requests/5" {
		t.Errorf("got %+v", got)
	}
}

func TestGitLabPrViewNotFound(t *testing.T) {
	f, info := fakeGitLab(t)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/9", http.StatusNotFound, `{"message":"404 Not found"}`)
	if _, err := ProviderGitLab.PrView(t.Context(), info, 9); !IsNotFound(err) {
		t.Errorf("err = %v, want not found", err)
	}
}

// gitlabPage writes one page of a GitLab offset-paginated listing. Like
// GitLab for collections over 10,000 items, it omits X-Total(-Pages).
func gitlabPage(w http.ResponseWriter, page, next string, body string) {
	w.Header().Set("X-Page", page)
	w.Header().Set("X-Per-Page", "2")
	if next != "" {
		w.Header().Set("X-Next-Page", next)
	}
	writeJSON(w, http.StatusOK, body)
}

func TestGitLabPrListPaginates(t *testing.T) {
	f, info := fakeGitLab(t)
	f.handle("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "opened" || q.Get("author_username") != "bob" || q.Get("target_branch") != "main" {
			t.Errorf("query = %v", q)
		}
		if q.Get("page") == "2" {
			gitlabPage(w, "2", "", `[{"iid":3,"title":"c","state":"opened","author":{"username":"bob"}}]`)
			return
		}
		gitlabPage(w, "1", "2", `[{"iid":1,"title":"a","state":"opened","author":{"username":"bob"}},
			{"iid":2,"title":"b","state":"opened","author":{"username":"bob"}}]`)
	})
	prs, err := ProviderGitLab.PrList(t.Context(), info, ListOptions{State: "open", Author: "bob", Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var numbers []int
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
	}
	if !reflect.DeepEqual(numbers, []int{1, 2, 3}) {
		t.Errorf("numbers = %v", numbers)
	}
}

func TestGitLabPrMergeOptions(t *testing.T) {
	for _, tc := range []struct {
		opts MergeOptions
		want map[string]any
	}{
		// Unset options must not override the MR's squash/remove-branch settings.
		{MergeOptions{}, map[string]any{}},
		{MergeOptions{Method: "merge", DeleteBranch: new(false)}, map[string]any{"squash": false, "should_remove_source_branch": false}},
		{MergeOptions{Method: "squash", DeleteBranch: new(true)}, map[string]any{"squash": true, "should_remove_source_branch": true}},
	} {
		f, info := fakeGitLab(t)
		f.handle("PUT /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/merge", func(w http.ResponseWriter, r *http.Request) {
			if got := decodeJSON(t, r); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%+v: payload = %v, want %v", tc.opts, got, tc.want)
			}
			writeJSON(w, http.StatusOK, strings.Replace(gitlabMR5, `"state":"opened"`, `"state":"merged","merged_at":"2026-10-03T10:00:00Z"`, 1))
		})
		d, err := ProviderGitLab.PrMerge(t.Context(), info, 5, tc.opts)
		if err != nil || !d.Merged {
			t.Errorf("%+v: got %+v, %v", tc.opts, d, err)
		}
	}
}

func TestGitLabPrMergeRebaseUnsupported(t *testing.T) {
	_, info := fakeGitLab(t)
	_, err := ProviderGitLab.PrMerge(t.Context(), info, 5, MergeOptions{Method: "rebase"})
	if _, ok := err.(*InvalidArgumentError); !ok {
		t.Errorf("err = %v, want invalid argument", err)
	}
}

func TestGitLabPrViewDraft(t *testing.T) {
	f, info := fakeGitLab(t)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5", http.StatusOK, gitlabMR5)
	if d, err := ProviderGitLab.PrView(t.Context(), info, 5); err != nil || !d.Draft {
		t.Errorf("got %+v, %v", d, err)
	}
}

func TestGitLabPrCreateDraft(t *testing.T) {
	for _, tc := range []struct {
		title string
		draft bool
		want  string
	}{
		{"Add y", false, "Add y"},
		{"Add y", true, "Draft: Add y"},
		{"draft: Add y", true, "draft: Add y"},
		{"[Draft] Add y", true, "[Draft] Add y"},
	} {
		f, info := fakeGitLab(t)
		f.handle("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests", func(w http.ResponseWriter, r *http.Request) {
			if q := r.URL.Query(); q.Get("state") != "opened" || q.Get("source_branch") != "feature/y" {
				t.Errorf("duplicate check query = %v", q)
			}
			writeJSON(w, http.StatusOK, `[]`)
		})
		f.handle("POST /api/v4/projects/group%2Fsub%2Fproj/merge_requests", func(w http.ResponseWriter, r *http.Request) {
			got := decodeJSON(t, r)
			if got["title"] != tc.want || got["source_branch"] != "feature/y" || got["target_branch"] != "main" {
				t.Errorf("payload = %v, want title %q", got, tc.want)
			}
			writeJSON(w, http.StatusCreated, gitlabMR5)
		})
		if _, err := ProviderGitLab.PrCreate(t.Context(), info, CreateOptions{Title: tc.title, Draft: tc.draft, Base: "main", Head: "feature/y"}); err != nil {
			t.Error(err)
		}
	}
}

func TestGitLabBranchListPaginates(t *testing.T) {
	f, info := fakeGitLab(t)
	f.handle("GET /api/v4/projects/group%2Fsub%2Fproj/repository/branches", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			gitlabPage(w, "2", "", `[{"name":"c","commit":{"author_name":"Bob","committed_date":"2026-10-03T10:00:00Z"}}]`)
			return
		}
		gitlabPage(w, "1", "2", `[{"name":"a"},{"name":"b"}]`)
	})
	branches, err := ProviderGitLab.BranchListRemote(t.Context(), info, BranchListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 3 || branches[2].Name != "c" || branches[2].Author != "Bob" {
		t.Errorf("branches = %+v", branches)
	}
}

// gitlabDiscussionsFixture has a resolved inline thread with a reply, a
// standalone (unresolvable) note and an unresolved general thread.
const gitlabDiscussionsFixture = `[
	{"id":"d1","notes":[
		{"id":11,"body":"rename this","author":{"username":"rev"},"created_at":"2026-10-01T10:00:00Z",
		 "resolvable":true,"resolved":true,"position":{"new_path":"a.go","new_line":3}},
		{"id":12,"body":"done","author":{"username":"me"},"resolvable":true,"resolved":true}]},
	{"id":"d2","individual_note":true,"notes":[
		{"id":21,"body":"nice","author":{"name":"Bob"}},
		{"id":22,"body":"added 1 commit","system":true}]}]`

func TestGitLabPrCommentsThreads(t *testing.T) {
	f, info := fakeGitLab(t)
	f.handle("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/discussions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			gitlabPage(w, "2", "", `[{"id":"d3","notes":[{"id":31,"body":"why?","author":{"username":"rev"},
				"resolvable":true,"resolved":false,"position":{"old_path":"b.go","old_line":7}}]}]`)
			return
		}
		gitlabPage(w, "1", "2", gitlabDiscussionsFixture)
	})
	comments, err := ProviderGitLab.PrComments(t.Context(), info, 5)
	if err != nil {
		t.Fatal(err)
	}
	base := info.HTTPBase + "/group/sub/proj/-/merge_requests/5#note_"
	want := []PRComment{
		{ID: 11, Author: "rev", Body: "rename this", CreatedAt: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
			Resolvable: true, Resolved: true, Path: "a.go", Line: 3, URL: base + "11"},
		{ID: 12, Author: "me", Body: "done", Resolved: true, URL: base + "12", ParentID: 11},
		{ID: 21, Author: "Bob", Body: "nice", URL: base + "21"},
		{ID: 31, Author: "rev", Body: "why?", Resolvable: true, Path: "b.go", Line: 7, URL: base + "31"},
	}
	if !reflect.DeepEqual(comments, want) {
		t.Errorf("comments:\n got %+v\nwant %+v", comments, want)
	}
}

func TestGitLabPrAddComment(t *testing.T) {
	f, info := fakeGitLab(t)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5", http.StatusOK,
		strings.Replace(gitlabMR5, `"iid":5,`, `"iid":5,"diff_refs":{"base_sha":"b","head_sha":"h","start_sha":"s"},`, 1))
	f.handle("POST /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/discussions", func(w http.ResponseWriter, r *http.Request) {
		want := map[string]any{"body": "fix", "position": map[string]any{
			"position_type": "text", "base_sha": "b", "head_sha": "h", "start_sha": "s",
			"old_path": "a.go", "new_path": "a.go", "new_line": float64(9),
		}}
		if got := decodeJSON(t, r); !reflect.DeepEqual(got, want) {
			t.Errorf("payload = %v", got)
		}
		writeJSON(w, http.StatusCreated, `{"id":"d9","notes":[{"id":91,"body":"fix","resolvable":true,
			"position":{"new_path":"a.go","new_line":9}}]}`)
	})
	c, err := ProviderGitLab.PrAddComment(t.Context(), info, 5, AddCommentOptions{Path: "a.go", Line: 9, Body: "fix"})
	if err != nil || c.ID != 91 || c.Path != "a.go" || c.Line != 9 || !c.Resolvable {
		t.Errorf("got %+v, %v", c, err)
	}
}

func TestGitLabPrReplyAndResolve(t *testing.T) {
	f, info := fakeGitLab(t)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/discussions", http.StatusOK, gitlabDiscussionsFixture)
	f.handle("POST /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/discussions/d1/notes", func(w http.ResponseWriter, r *http.Request) {
		if got := decodeJSON(t, r); got["body"] != "ok" {
			t.Errorf("payload = %v", got)
		}
		writeJSON(w, http.StatusCreated, `{"id":13,"body":"ok","author":{"username":"me"}}`)
	})
	// Replying to a reply (12) goes to its discussion; the reply's parent is the root.
	c, err := ProviderGitLab.PrReplyComment(t.Context(), info, 5, 12, "ok")
	if err != nil || c.ID != 13 || c.ParentID != 11 {
		t.Errorf("reply = %+v, %v", c, err)
	}

	f.handle("PUT /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/discussions/d1", func(w http.ResponseWriter, r *http.Request) {
		if got := decodeJSON(t, r); got["resolved"] != true {
			t.Errorf("payload = %v", got)
		}
		writeJSON(w, http.StatusOK, `{"id":"d1","notes":[]}`)
	})
	if err := ProviderGitLab.PrResolveComment(t.Context(), info, 5, 11); err != nil {
		t.Error(err)
	}
	if err := ProviderGitLab.PrResolveComment(t.Context(), info, 5, 21); !isInvalidArg(err) {
		t.Errorf("resolving a standalone note: err = %v", err)
	}
	if err := ProviderGitLab.PrResolveComment(t.Context(), info, 5, 99); !IsNotFound(err) {
		t.Errorf("unknown note: err = %v", err)
	}
}

func isInvalidArg(err error) bool {
	_, ok := err.(*InvalidArgumentError)
	return ok
}

func TestGitLabPrListStates(t *testing.T) {
	for state, apiState := range map[string]string{"open": "opened", "closed": "closed", "merged": "merged", "all": ""} {
		f, info := fakeGitLab(t)
		f.handle("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests", func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("state"); got != apiState {
				t.Errorf("%s: API state = %q, want %q", state, got, apiState)
			}
			writeJSON(w, http.StatusOK, `[{"iid":1,"state":"locked"}]`)
		})
		prs, err := ProviderGitLab.PrList(t.Context(), info, ListOptions{State: state})
		if err != nil || len(prs) != 1 || prs[0].State != "open" || prs[0].ProviderState != "locked" {
			t.Errorf("%s: got %+v, %v", state, prs, err)
		}
	}
}

// fakeGitLabRename serves the requests of a branch rename old -> new. MR 3
// targets old; sourceMRs are the MRs whose source branch is old.
func fakeGitLabRename(t *testing.T, sourceMRs string, updateStatus int) (*fakeAPI, *Info) {
	f, info := fakeGitLab(t)
	const mrs = "GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests"
	f.handle(mrs, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("state") != "opened":
			t.Errorf("query = %v", q)
		case q.Get("source_branch") == "old":
			writeJSON(w, http.StatusOK, sourceMRs)
		case q.Get("target_branch") == "old":
			writeJSON(w, http.StatusOK, `[{"iid":3,"title":"T","description":"D","source_branch":"feature"}]`)
		default:
			t.Errorf("query = %v", q)
		}
	})
	f.reply("POST /api/v4/projects/group%2Fsub%2Fproj/repository/branches", http.StatusCreated, `{"name":"new"}`)
	f.handle("PUT /api/v4/projects/group%2Fsub%2Fproj/merge_requests/3", func(w http.ResponseWriter, r *http.Request) {
		got := decodeJSON(t, r)
		if got["target_branch"] == "new" {
			writeJSON(w, updateStatus, `{"iid":3}`)
			return
		}
		// Fallback: the old MR is closed and points to its replacement.
		if got["state_event"] != "close" || got["description"] != "D\n\nSuperseded by https://gl/mr/4" {
			t.Errorf("close payload = %v", got)
		}
		writeJSON(w, http.StatusOK, `{"iid":3}`)
	})
	f.reply("DELETE /api/v4/projects/group%2Fsub%2Fproj/repository/branches/old", http.StatusNoContent, ``)
	return f, info
}

func TestGitLabBranchRenameRetargetsMRs(t *testing.T) {
	f, info := fakeGitLabRename(t, `[]`, http.StatusOK)
	if err := ProviderGitLab.BranchRename(t.Context(), info, "old", "new", BranchRenameOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.called("POST /api/v4/projects/group%2Fsub%2Fproj/merge_requests") {
		t.Error("MR recreated although the update succeeded")
	}
	if !f.called("DELETE /api/v4/projects/group%2Fsub%2Fproj/repository/branches/old") {
		t.Error("old branch not deleted")
	}
}

func TestGitLabBranchRenameRecreatesMRAsFallback(t *testing.T) {
	f, info := fakeGitLabRename(t, `[]`, http.StatusConflict)
	f.handle("POST /api/v4/projects/group%2Fsub%2Fproj/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		got := decodeJSON(t, r)
		if got["target_branch"] != "new" || got["source_branch"] != "feature" || got["title"] != "T" {
			t.Errorf("recreate payload = %v", got)
		}
		writeJSON(w, http.StatusCreated, `{"iid":4,"web_url":"https://gl/mr/4"}`)
	})
	if err := ProviderGitLab.BranchRename(t.Context(), info, "old", "new", BranchRenameOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestGitLabBranchRenameSourceMRs(t *testing.T) {
	f, info := fakeGitLabRename(t, `[{"iid":8,"source_branch":"old"}]`, http.StatusOK)
	err := ProviderGitLab.BranchRename(t.Context(), info, "old", "new", BranchRenameOptions{})
	if !isInvalidArg(err) || !strings.Contains(err.Error(), "!8") {
		t.Fatalf("err = %v", err)
	}
	if f.called("POST /api/v4/projects/group%2Fsub%2Fproj/repository/branches") {
		t.Error("branch created despite refusal")
	}
	if err := ProviderGitLab.BranchRename(t.Context(), info, "old", "new", BranchRenameOptions{Force: true}); err != nil {
		t.Errorf("--force: %v", err)
	}
}
