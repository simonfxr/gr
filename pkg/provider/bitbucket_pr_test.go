package provider

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const bitbucketPR7 = `{"id":7,"title":"Add z","description":"desc","state":"OPEN","draft":true,
	"author":{"nickname":"carol","display_name":"Carol"},
	"created_on":"2026-10-01T10:00:00.000000+00:00","updated_on":"2026-10-02T10:00:00.000000+00:00",
	"source":{"branch":{"name":"feature/z"}},"destination":{"branch":{"name":"main"}},
	"links":{"html":{"href":"https://bitbucket.org/workspace/repo/pull-requests/7"}}}`

func TestBitbucketPrView(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	f.reply("GET /repositories/workspace/repo/pullrequests/7", http.StatusOK, bitbucketPR7)
	got, err := ProviderBitbucket.PrView(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 7 || got.Title != "Add z" || got.Body != "desc" || got.Author != "carol" ||
		got.Base != "main" || got.Head != "feature/z" || got.Merged || !got.Draft ||
		!got.CreatedAt.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) ||
		got.URL != "https://bitbucket.org/workspace/repo/pull-requests/7" {
		t.Errorf("got %+v", got)
	}
}

func TestBitbucketPrCreate(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	f.handle("GET /repositories/workspace/repo/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("state") != "OPEN" || q.Get("q") != `source.branch.name = "feature/z"` {
			t.Errorf("query = %v", q)
		}
		writeJSON(w, http.StatusOK, `{"values":[]}`)
	})
	f.handle("POST /repositories/workspace/repo/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		got := decodeJSON(t, r)
		want := map[string]any{
			"title": "Add z", "description": "desc", "draft": true, "close_source_branch": true,
			"source":      map[string]any{"branch": map[string]any{"name": "feature/z"}},
			"destination": map[string]any{"branch": map[string]any{"name": "main"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("payload = %v", got)
		}
		writeJSON(w, http.StatusCreated, bitbucketPR7)
	})
	d, err := ProviderBitbucket.PrCreate(t.Context(), info, CreateOptions{
		Title: "Add z", Body: "desc", Base: "main", Head: "feature/z", Draft: true, DeleteAfterMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Number != 7 || !d.Draft || d.Head != "feature/z" {
		t.Errorf("got %+v", d)
	}
}

func TestBitbucketPrCreateRefusesDuplicate(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	f.reply("GET /repositories/workspace/repo/pullrequests", http.StatusOK, `{"values":[{"id":3}]}`)
	_, err := ProviderBitbucket.PrCreate(t.Context(), info, CreateOptions{Title: "x", Base: "main", Head: "feature/z"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
}

func TestBitbucketPrMergeAndClose(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	merged := strings.Replace(bitbucketPR7, `"OPEN"`, `"MERGED"`, 1)
	f.handle("POST /repositories/workspace/repo/pullrequests/7/merge", func(w http.ResponseWriter, r *http.Request) {
		if got := decodeJSON(t, r); !reflect.DeepEqual(got, map[string]any{"close_source_branch": true}) {
			t.Errorf("payload = %v", got)
		}
		writeJSON(w, http.StatusOK, merged)
	})
	d, err := ProviderBitbucket.PrMerge(t.Context(), info, 7, MergeOptions{DeleteBranch: new(true)})
	if err != nil || !d.Merged || d.State != "merged" {
		t.Errorf("merge = %+v, %v", d, err)
	}

	f.reply("POST /repositories/workspace/repo/pullrequests/8/decline", http.StatusOK,
		strings.Replace(strings.Replace(bitbucketPR7, `"OPEN"`, `"DECLINED"`, 1), `"id":7`, `"id":8`, 1))
	d, err = ProviderBitbucket.PrClose(t.Context(), info, 8, CloseOptions{})
	if err != nil || d.Number != 8 || d.State != "closed" || d.ProviderState != "declined" {
		t.Errorf("close = %+v, %v", d, err)
	}
}

// TestBitbucketPrCloseDeletesForkBranch deletes the source branch in the
// fork, not a same-named branch of the destination repository.
func TestBitbucketPrCloseDeletesForkBranch(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	declined := strings.Replace(strings.Replace(bitbucketPR7, `"OPEN"`, `"DECLINED"`, 1),
		`"source":{"branch":{"name":"feature/z"}}`,
		`"source":{"branch":{"name":"feature/z"},"repository":{"full_name":"alice/repo-fork"}}`, 1)
	f.reply("POST /repositories/workspace/repo/pullrequests/7/decline", http.StatusOK, declined)
	f.reply("GET /repositories/alice/repo-fork", http.StatusOK, `{"mainbranch":{"name":"main"}}`)
	f.reply("DELETE /repositories/alice/repo-fork/refs/branches/feature/z", http.StatusNoContent, ``)
	if _, err := ProviderBitbucket.PrClose(t.Context(), info, 7, CloseOptions{DeleteBranch: true}); err != nil {
		t.Fatal(err)
	}
	if !f.called("DELETE /repositories/alice/repo-fork/refs/branches/feature/z") {
		t.Error("fork branch not deleted")
	}
}

func TestBitbucketPrMergeQueued(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	f.handle("POST /repositories/workspace/repo/pullrequests/7/merge", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://api.bitbucket.org/2.0/repositories/workspace/repo/pullrequests/7/merge/task-status/1")
		writeJSON(w, http.StatusAccepted, `{"task_status":"PENDING"}`)
	})
	f.reply("GET /repositories/workspace/repo/pullrequests/7", http.StatusOK, bitbucketPR7)
	d, err := ProviderBitbucket.PrMerge(t.Context(), info, 7, MergeOptions{})
	if err != nil || d.Merged || d.State != "open" {
		t.Errorf("merge = %+v, %v", d, err)
	}
}

func TestBitbucketResolveCommentWithTokenOnly(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	f.reply("POST /repositories/workspace/repo/pullrequests/7/comments/5/resolve", http.StatusOK, `{}`)
	if err := ProviderBitbucket.PrResolveComment(t.Context(), info, 7, 5); err != nil {
		t.Fatal(err)
	}
}

func TestBitbucketBranchDelete(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	f.reply("GET /repositories/workspace/repo", http.StatusOK, `{"mainbranch":{"name":"main"}}`)
	f.reply("DELETE /repositories/workspace/repo/refs/branches/feature/z", http.StatusNoContent, ``)
	if err := ProviderBitbucket.BranchDelete(t.Context(), info, "main", BranchDeleteOptions{}); err == nil {
		t.Error("deleting the main branch must be refused")
	}
	if err := ProviderBitbucket.BranchDelete(t.Context(), info, "feature/z", BranchDeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !f.called("DELETE /repositories/workspace/repo/refs/branches/feature/z") {
		t.Error("branch not deleted")
	}
}

func TestBitbucketBranchRenameRetargetsPRs(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	f.reply("GET /repositories/workspace/repo/refs/branches/old", http.StatusOK, `{"target":{"hash":"abc123"}}`)
	f.handle("POST /repositories/workspace/repo/refs/branches", func(w http.ResponseWriter, r *http.Request) {
		if got := decodeJSON(t, r); !reflect.DeepEqual(got, map[string]any{"name": "new", "target": map[string]any{"hash": "abc123"}}) {
			t.Errorf("create payload = %v", got)
		}
		writeJSON(w, http.StatusCreated, `{"name":"new"}`)
	})
	f.handle("GET /repositories/workspace/repo/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		switch q := r.URL.Query().Get("q"); q {
		case `destination.branch.name = "old"`:
			writeJSON(w, http.StatusOK, `{"values":[{"id":1}]}`)
		case `source.branch.name = "old"`:
			writeJSON(w, http.StatusOK, `{"values":[]}`)
		default:
			t.Errorf("q = %q", q)
		}
	})
	f.reply("GET /repositories/workspace/repo/pullrequests/1", http.StatusOK, `{"id":1,"title":"T","description":"D",
		"draft":true,"close_source_branch":true,"reviewers":[{"uuid":"{r1}"}],
		"source":{"branch":{"name":"feature"},"repository":{"full_name":"workspace/repo"}},
		"destination":{"branch":{"name":"old"}}}`)
	f.handle("PUT /repositories/workspace/repo/pullrequests/1", func(w http.ResponseWriter, r *http.Request) {
		want := map[string]any{
			"title": "T", "description": "D", "draft": true, "close_source_branch": true,
			"reviewers":   []any{map[string]any{"uuid": "{r1}"}},
			"source":      map[string]any{"branch": map[string]any{"name": "feature"}, "repository": map[string]any{"full_name": "workspace/repo"}},
			"destination": map[string]any{"branch": map[string]any{"name": "new"}},
		}
		if got := decodeJSON(t, r); !reflect.DeepEqual(got, want) {
			t.Errorf("update payload = %v", got)
		}
		writeJSON(w, http.StatusOK, `{"id":1}`)
	})
	f.reply("DELETE /repositories/workspace/repo/refs/branches/old", http.StatusNoContent, ``)
	if err := ProviderBitbucket.BranchRename(t.Context(), info, "old", "new", BranchRenameOptions{}); err != nil {
		t.Fatal(err)
	}
	if !f.called("PUT /repositories/workspace/repo/pullrequests/1") || !f.called("DELETE /repositories/workspace/repo/refs/branches/old") {
		t.Error("PR not retargeted or old branch not deleted")
	}
}

func TestBitbucketPrMergeOptions(t *testing.T) {
	for _, tc := range []struct {
		opts MergeOptions
		want map[string]any
	}{
		// Unset options fall back to the PR's close_source_branch setting.
		{MergeOptions{}, map[string]any{}},
		{MergeOptions{Method: "merge", DeleteBranch: new(false)}, map[string]any{"merge_strategy": "merge_commit", "close_source_branch": false}},
		{MergeOptions{Method: "squash"}, map[string]any{"merge_strategy": "squash"}},
		{MergeOptions{Method: "rebase"}, map[string]any{"merge_strategy": "rebase_fast_forward"}},
	} {
		f, info := fakeBitbucketAPI(t)
		f.handle("POST /repositories/workspace/repo/pullrequests/7/merge", func(w http.ResponseWriter, r *http.Request) {
			if got := decodeJSON(t, r); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%+v: payload = %v, want %v", tc.opts, got, tc.want)
			}
			writeJSON(w, http.StatusOK, strings.Replace(bitbucketPR7, `"OPEN"`, `"MERGED"`, 1))
		})
		if _, err := ProviderBitbucket.PrMerge(t.Context(), info, 7, tc.opts); err != nil {
			t.Error(err)
		}
	}
}

func TestBitbucketPrListRejectsAssignee(t *testing.T) {
	_, info := fakeBitbucketAPI(t)
	if _, err := ProviderBitbucket.PrList(t.Context(), info, ListOptions{Assignee: "me"}); !IsUnsupported(err) {
		t.Errorf("err = %v, want unsupported", err)
	}
}
