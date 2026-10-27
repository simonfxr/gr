package provider

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const githubPR7 = `{"number":7,"title":"Add x","body":"desc","state":"open","draft":true,
	"user":{"login":"alice"},"created_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-02T10:00:00Z",
	"base":{"ref":"main","repo":{"name":"repo","owner":{"login":"owner"}}},
	"head":{"ref":"feature/x","repo":{"name":"repo","owner":{"login":"owner"}}},
	"html_url":"https://github.example/owner/repo/pull/7"}`

func TestGitHubPrView(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, githubPR7)
	got, err := ProviderGitHub.PrView(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := &PRDetails{
		Number: 7, Title: "Add x", Body: "desc", Author: "alice", State: "open",
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
			pr := strings.Replace(githubPR7, `"head":{"ref":"feature/x","repo":{"name":"repo","owner":{"login":"owner"}}}`,
				`"head":{"ref":"feature/x","repo":`+tc.headRepo+`}`, 1)
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

func TestGitHubPrMergeReportsFailedBranchDeletion(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, githubPR7)
	f.reply("PUT /api/v3/repos/owner/repo/pulls/7/merge", http.StatusOK, `{"merged":true}`)
	f.reply("DELETE /api/v3/repos/owner/repo/git/refs/heads/feature/x", http.StatusUnprocessableEntity, `{"message":"Reference does not exist"}`)
	d, err := ProviderGitHub.PrMerge(t.Context(), info, 7, MergeOptions{DeleteBranch: new(true)})
	if d == nil || err == nil || !strings.Contains(err.Error(), "merged, but deleting branch") {
		t.Errorf("got %v, %v", d, err)
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
