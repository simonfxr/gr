package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func newTaskTestServer(t *testing.T, handler http.HandlerFunc) *Info {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("BITBUCKET_API_BASE_URL", server.URL)
	t.Setenv("BITBUCKET_TOKEN", "test-token")
	t.Setenv("BITBUCKET_USERNAME", "")
	return &Info{Provider: ProviderBitbucket, Owner: "workspace", Repo: "repo"}
}

func TestBitbucketPrTasksPaginationAndState(t *testing.T) {
	info := newTaskTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repositories/workspace/repo/pullrequests/7/tasks" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("q"); got != `state = "UNRESOLVED"` {
			t.Errorf("q = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, `{"values":[{"id":2,"state":"UNRESOLVED","pending":true,"content":{"raw":"second"}}]}`)
			return
		}
		io.WriteString(w, `{"next":"/repositories/workspace/repo/pullrequests/7/tasks?page=2&q=state+%3D+%22UNRESOLVED%22",
			"values":[{"id":1,"state":"UNRESOLVED","created_on":"2026-10-27T10:00:00+00:00",
				"content":{"raw":"first"},"creator":{"display_name":"Simon"},"comment":{"id":99},
				"resolved_by":{"nickname":"stale"}}]}`)
	})
	tasks, err := ProviderBitbucket.PrTasks(context.Background(), info, 7, TaskListOptions{State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 2 && !tasks[0].CreatedAt.Equal(time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("created_at = %v", tasks[0].CreatedAt)
	}
	if len(tasks) == 2 {
		tasks[0].CreatedAt = time.Time{}
	}
	want := []PRTask{
		// resolved_by is dropped for unresolved tasks.
		{ID: 1, Body: "first", Creator: "Simon", CommentID: 99},
		{ID: 2, Body: "second", Pending: true},
	}
	if !reflect.DeepEqual(tasks, want) {
		t.Errorf("tasks = %+v\nwant %+v", tasks, want)
	}
}

func TestBitbucketPrTasksInvalidState(t *testing.T) {
	info := newTaskTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
	})
	if _, err := ProviderBitbucket.PrTasks(context.Background(), info, 1, TaskListOptions{State: "bogus"}); err == nil {
		t.Fatal("expected error for invalid state")
	}
}

func TestBitbucketPrAddTask(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts AddTaskOptions
		want map[string]any
	}{
		{"plain", AddTaskOptions{Body: "do it"}, map[string]any{"content": map[string]any{"raw": "do it"}}},
		{"comment+pending", AddTaskOptions{Body: "do it", CommentID: 5, Pending: true}, map[string]any{
			"content": map[string]any{"raw": "do it"}, "comment": map[string]any{"id": float64(5)}, "pending": true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := newTaskTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/repositories/workspace/repo/pullrequests/3/tasks" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q", ct)
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("payload = %v, want %v", got, tc.want)
				}
				w.WriteHeader(http.StatusCreated)
				io.WriteString(w, `{"id":11,"state":"UNRESOLVED","content":{"raw":"do it"}}`)
			})
			task, err := ProviderBitbucket.PrAddTask(context.Background(), info, 3, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if task.ID != 11 || task.Body != "do it" || task.Resolved {
				t.Errorf("task = %+v", task)
			}
		})
	}
}

func TestBitbucketPrAddTaskEmptyBody(t *testing.T) {
	if _, err := ProviderBitbucket.PrAddTask(context.Background(), &Info{Provider: ProviderBitbucket}, 1, AddTaskOptions{Body: "  "}); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestBitbucketPrSetTaskResolved(t *testing.T) {
	for _, resolved := range []bool{true, false} {
		wantState := "UNRESOLVED"
		if resolved {
			wantState = "RESOLVED"
		}
		t.Run(wantState, func(t *testing.T) {
			info := newTaskTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/repositories/workspace/repo/pullrequests/3/tasks/11" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, map[string]any{"state": wantState}) {
					t.Errorf("payload = %v", got)
				}
				io.WriteString(w, `{"id":11,"state":"`+wantState+`","content":{"raw":"x"},
					"resolved_by":{"nickname":"me"},"resolved_on":"2026-10-27T11:00:00Z"}`)
			})
			task, err := ProviderBitbucket.PrSetTaskResolved(context.Background(), info, 3, 11, resolved)
			if err != nil {
				t.Fatal(err)
			}
			if task.Resolved != resolved || (task.ResolvedBy == "me") != resolved || task.ResolvedAt.IsZero() == resolved {
				t.Errorf("task = %+v", task)
			}
		})
	}
}

func TestBitbucketPrTasksAPIError(t *testing.T) {
	info := newTaskTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"nope"}}`, http.StatusForbidden)
	})
	_, err := ProviderBitbucket.PrTasks(context.Background(), info, 1, TaskListOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}
