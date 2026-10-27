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

func TestGitLabPrCommentsPaginates(t *testing.T) {
	f, info := fakeGitLab(t)
	f.handle("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5/notes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			gitlabPage(w, "2", "", `[{"id":3,"body":"third","author":{"username":"bob"}}]`)
			return
		}
		gitlabPage(w, "1", "2", `[{"id":1,"body":"first","author":{"username":"bob"}},
			{"id":2,"body":"assigned","system":true,"author":{"username":"bob"}}]`)
	})
	comments, err := ProviderGitLab.PrComments(t.Context(), info, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 3 {
		t.Errorf("comments = %+v", comments)
	}
}
