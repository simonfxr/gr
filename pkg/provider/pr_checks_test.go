package provider

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// checkStates returns "name=state" for each check, in report order.
func checkStates(r *ChecksReport) []string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Name+"="+c.State)
	}
	return out
}

func TestGitHubPrChecks(t *testing.T) {
	f, info := fakeGitHub(t)
	f.reply("GET /api/v3/repos/owner/repo/pulls/7", http.StatusOK, githubPR7)
	f.handle("GET /api/v3/repos/owner/repo/commits/abc/check-runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "latest" {
			t.Errorf("query = %v", r.URL.Query())
		}
		if r.URL.Query().Get("page") != "2" {
			w.Header().Set("Link", `<`+f.srv.URL+`/api/v3/repos/owner/repo/commits/abc/check-runs?filter=latest&page=2>; rel="next"`)
			writeJSON(w, http.StatusOK, `{"total_count":3,"check_runs":[
				{"name":"build","status":"completed","conclusion":"success","details_url":"https://ci/build"},
				{"name":"lint","status":"completed","conclusion":"timed_out","output":{"title":"took too long"}}]}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"total_count":3,"check_runs":[{"name":"docs","status":"completed","conclusion":"skipped"}]}`)
	})
	f.reply("GET /api/v3/repos/owner/repo/commits/abc/status", http.StatusOK,
		`{"state":"pending","statuses":[{"context":"ci/jenkins","state":"pending","target_url":"https://jenkins/1","description":"running"}]}`)
	r, err := ProviderGitHub.PrChecks(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lint=fail", "ci/jenkins=pending", "build=pass", "docs=skipped"}
	if got := checkStates(r); !reflect.DeepEqual(got, want) {
		t.Errorf("checks = %v, want %v", got, want)
	}
	if r.State != CheckFail || r.Commit != "abc" || r.Pending() != 1 {
		t.Errorf("report = %+v", r)
	}
	if c := r.Checks[0]; c.ProviderState != "timed_out" || c.Description != "took too long" {
		t.Errorf("lint = %+v", c)
	}
}

func TestGitLabPrChecks(t *testing.T) {
	f, info := fakeGitLab(t)
	mr := strings.Replace(gitlabMR5, `"state":"opened"`, `"state":"opened","sha":"head1",
		"head_pipeline":{"id":11,"project_id":42,"sha":"head1","status":"running","web_url":"https://gl/p/11"}`, 1)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5", http.StatusOK, mr)
	// The pipeline runs in project 42, e.g. the fork of a fork MR.
	f.reply("GET /api/v4/projects/42/pipelines/11/jobs", http.StatusOK, `[
		{"name":"unit","stage":"test","status":"success"},
		{"name":"flaky","stage":"test","status":"failed","allow_failure":true},
		{"name":"deploy","stage":"deploy","status":"manual"},
		{"name":"e2e","stage":"test","status":"running"}]`)
	f.reply("GET /api/v4/projects/42/pipelines/11/bridges", http.StatusOK, `[{"name":"child","stage":"test","status":"success"}]`)
	r, err := ProviderGitLab.PrChecks(t.Context(), info, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"flaky=fail", "e2e=pending", "child=pass", "unit=pass", "deploy=skipped"}
	if got := checkStates(r); !reflect.DeepEqual(got, want) {
		t.Errorf("checks = %v, want %v", got, want)
	}
	// An allowed failure does not fail the checks; e2e is still running.
	if r.State != CheckPending || r.URL != "https://gl/p/11" || r.Commit != "head1" || !r.Checks[0].Optional {
		t.Errorf("report = %+v", r)
	}
}

func TestGitLabPrChecksWithoutPipeline(t *testing.T) {
	f, info := fakeGitLab(t)
	f.reply("GET /api/v4/projects/group%2Fsub%2Fproj/merge_requests/5", http.StatusOK, gitlabMR5)
	r, err := ProviderGitLab.PrChecks(t.Context(), info, 5)
	if err != nil || r.State != ChecksNone || r.Checks == nil {
		t.Errorf("report = %+v, %v", r, err)
	}
}

func TestBitbucketPrChecks(t *testing.T) {
	f, info := fakeBitbucketAPI(t)
	pr := strings.Replace(bitbucketPR7, `"source":{"branch":{"name":"feature/z"}}`,
		`"source":{"branch":{"name":"feature/z"},"commit":{"hash":"abc123def456"}}`, 1)
	f.reply("GET /repositories/workspace/repo/pullrequests/7", http.StatusOK, pr)
	commit := func(hash string) string {
		return `"links":{"commit":{"href":"https://api.bitbucket.org/2.0/repositories/workspace/repo/commit/` + hash + `"}}`
	}
	f.reply("GET /repositories/workspace/repo/pullrequests/7/statuses", http.StatusOK, `{"values":[
		{"key":"build","name":"Build #1","state":"FAILED",`+commit("0000000000000000000000000000000000000000")+`},
		{"key":"build","name":"Build #2","state":"SUCCESSFUL","url":"https://ci/2",`+commit("abc123def4567890abc123def4567890abc12345")+`},
		{"key":"deploy","name":"Deploy","state":"STOPPED",`+commit("abc123def4567890abc123def4567890abc12345")+`}]}`)
	r, err := ProviderBitbucket.PrChecks(t.Context(), info, 7)
	if err != nil {
		t.Fatal(err)
	}
	// Build #1 ran on an earlier commit of the PR.
	want := []string{"Deploy=cancelled", "Build #2=pass"}
	if got := checkStates(r); !reflect.DeepEqual(got, want) {
		t.Errorf("checks = %v, want %v", got, want)
	}
	if r.State != CheckFail || r.Commit != "abc123def456" {
		t.Errorf("report = %+v", r)
	}
}
