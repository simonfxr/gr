package provider

import (
	"net/http"
	"reflect"
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

// gitlabPage writes one page of a GitLab offset-paginated listing.
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
		w.Header().Set("X-Total-Pages", "2")
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
