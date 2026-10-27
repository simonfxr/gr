package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/simonfxr/gr/pkg/provider"
)

// TestMain lets end-to-end tests re-execute the test binary as the gr CLI.
func TestMain(m *testing.M) {
	if os.Getenv("GR_TEST_RUN_MAIN") == "1" {
		os.Args = append([]string{"gr"}, strings.Fields(os.Getenv("GR_TEST_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var bitbucketInfo = &provider.Info{Provider: provider.ProviderBitbucket, Owner: "workspace", Repo: "repo"}

// fakeBitbucket serves handler as the Bitbucket API for the duration of the test.
func fakeBitbucket(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("BITBUCKET_API_BASE_URL", server.URL)
	t.Setenv("BITBUCKET_TOKEN", "test-token")
	t.Setenv("BITBUCKET_USERNAME", "")
}

func TestParsePRRef(t *testing.T) {
	gitlab := &provider.Info{Provider: provider.ProviderGitLab, Owner: "group/sub", Repo: "proj"}
	for _, tc := range []struct {
		ref  string
		info *provider.Info
		want int // 0: usage error expected
	}{
		{"42", bitbucketInfo, 42},
		{"#42", bitbucketInfo, 42},
		{"0", bitbucketInfo, 0},
		{"-3", bitbucketInfo, 0},
		{"abc", bitbucketInfo, 0},
		{"https://bitbucket.org/workspace/repo/pull-requests/1282", bitbucketInfo, 1282},
		{"https://bitbucket.org/Workspace/Repo/pull-requests/1282/diff#comment-5", bitbucketInfo, 1282},
		{"bitbucket.org/workspace/repo/pull-requests/7", bitbucketInfo, 7},
		{"https://bitbucket.org/%7Bws-uuid%7D/%7Brepo-uuid%7D/pull-requests/1282", bitbucketInfo, 1282},
		{"https://bitbucket.org/other/repo/pull-requests/1282", bitbucketInfo, 0},
		{"https://bitbucket.org/workspace/other/pull-requests/1282", bitbucketInfo, 0},
		{"https://bitbucket.org/workspace/repo/src/main", bitbucketInfo, 0},
		{"https://bitbucket.org/workspace/repo/pull-requests/new", bitbucketInfo, 0},
		{"https://github.com/workspace/repo/pull/12/files", bitbucketInfo, 12},
		{"https://gitlab.com/group/sub/proj/-/merge_requests/5", gitlab, 5},
		{"https://gitlab.com/group/proj/-/merge_requests/5", gitlab, 0},
	} {
		got, err := parsePRRef(tc.ref, tc.info)
		if tc.want == 0 {
			if err == nil || errorKind(err) != kindUsage {
				t.Errorf("parsePRRef(%q) = %d, %v; want usage error", tc.ref, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parsePRRef(%q) = %d, %v; want %d", tc.ref, got, err, tc.want)
		}
	}
}

func TestResolvePRBranchLookup(t *testing.T) {
	var rows string
	fakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repositories/workspace/repo/pullrequests" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("q"); q != `source.branch.name = "feature/x"` {
			t.Errorf("q = %q", q)
		}
		io.WriteString(w, `{"values":[`+rows+`]}`)
	})
	ctx := t.Context()
	sel := PRSelector{Branch: "feature/x"}

	rows = `{"id":5,"title":"Five","state":"OPEN"}`
	if n, err := resolvePR(ctx, bitbucketInfo, "", sel); err != nil || n != 5 {
		t.Errorf("single match: got %d, %v", n, err)
	}

	rows = ``
	_, err := resolvePR(ctx, bitbucketInfo, "", sel)
	if errorKind(err) != kindNotFound || exitCode(err) != exitNotFound {
		t.Errorf("no match: got %v (kind %s)", err, errorKind(err))
	}

	rows = `{"id":5,"title":"Five"},{"id":6,"title":"Six"}`
	_, err = resolvePR(ctx, bitbucketInfo, "", sel)
	ce, ok := errors.AsType[*cliError](err)
	if !ok || ce.kind != kindAmbiguous || len(ce.candidates) != 2 || exitCode(err) != exitNotFound {
		t.Errorf("ambiguous: got %#v", err)
	}
}

func TestResolvePRConflicts(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		positional string
		sel        PRSelector
	}{
		{"1", PRSelector{Number: "2"}},
		{"1", PRSelector{Branch: "x"}},
		{"", PRSelector{Number: "1", Branch: "x"}},
	} {
		if _, err := resolvePR(ctx, bitbucketInfo, tc.positional, tc.sel); errorKind(err) != kindUsage {
			t.Errorf("resolvePR(%q, %+v) = %v; want usage error", tc.positional, tc.sel, err)
		}
	}
	// The same PR given twice, in any spelling, is fine.
	if n, err := resolvePR(ctx, bitbucketInfo, "3", PRSelector{Number: "https://bitbucket.org/workspace/repo/pull-requests/3"}); err != nil || n != 3 {
		t.Errorf("identical refs: got %d, %v", n, err)
	}
}

func TestErrorReporting(t *testing.T) {
	ambiguous := &cliError{kind: kindAmbiguous, msg: "found 2", candidates: []provider.PullRequest{{Number: 1, Title: "A"}, {Number: 2, Title: "B"}}}
	for _, tc := range []struct {
		err      error
		kind     string
		exitCode int
	}{
		{errors.New("boom"), kindError, exitError},
		{usageErrorf("bad"), kindUsage, exitUsage},
		{fmt.Errorf("wrapped: %w", notFoundErrorf("gone")), kindNotFound, exitNotFound},
		{ambiguous, kindAmbiguous, exitNotFound},
	} {
		if k, c := errorKind(tc.err), exitCode(tc.err); k != tc.kind || c != tc.exitCode {
			t.Errorf("%v: kind %s exit %d; want %s %d", tc.err, k, c, tc.kind, tc.exitCode)
		}
	}

	var buf bytes.Buffer
	reportError(&buf, ambiguous, true)
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON error: %v\n%s", err, buf.String())
	}
	if got["error"] != "found 2" || got["kind"] != "ambiguous" || got["exit_code"] != float64(3) || len(got["candidates"].([]any)) != 2 {
		t.Errorf("JSON error = %v", got)
	}

	buf.Reset()
	reportError(&buf, ambiguous, false)
	if want := "Error: found 2\n  #1 A ()\n  #2 B ()\n"; buf.String() != want {
		t.Errorf("text error = %q, want %q", buf.String(), want)
	}
}

func TestProviderErrorsMapToExitCodes(t *testing.T) {
	fakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
	})
	err := runPR(&PRCmd{Tasks: &PRTasksCmd{PRSelector: PRSelector{Number: "9999"}}}, bitbucketInfo)
	if exitCode(err) != exitNotFound {
		t.Errorf("404: got %v (exit %d)", err, exitCode(err))
	}
	err = runPR(&PRCmd{Tasks: &PRTasksCmd{PRSelector: PRSelector{Number: "1"}, State: "bogus"}}, bitbucketInfo)
	if exitCode(err) != exitUsage {
		t.Errorf("invalid state: got %v (exit %d)", err, exitCode(err))
	}
}

func TestWantsJSON(t *testing.T) {
	if !wantsJSON(&PRTasksCmd{JSON: true}) || wantsJSON(&PRTasksCmd{}) || wantsJSON(&PRBrowseCmd{}) || wantsJSON(nil) {
		t.Error("wantsJSON mismatch")
	}
}

func TestResolveBody(t *testing.T) {
	file := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(file, []byte("    indented `code`\nline 2\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveBody("inline", "", "x"); err != nil || got != "inline" {
		t.Errorf("inline: %q, %v", got, err)
	}
	if got, err := resolveBody("", file, "x"); err != nil || got != "    indented `code`\nline 2" {
		t.Errorf("file: %q, %v", got, err)
	}
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader("from stdin\n")
	if got, err := resolveBody("", "-", "x"); err != nil || got != "from stdin" {
		t.Errorf("stdin: %q, %v", got, err)
	}
	for _, tc := range [][2]string{{"a", file}, {"", ""}, {"  ", ""}} {
		if _, err := resolveBody(tc[0], tc[1], "x"); errorKind(err) != kindUsage {
			t.Errorf("resolveBody(%q, %q) = %v; want usage error", tc[0], tc[1], err)
		}
	}
}

func TestAddCommentsReportsPerItemResults(t *testing.T) {
	fakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Inline struct {
				Path string `json:"path"`
				To   int    `json:"to"`
			} `json:"inline"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Inline.Path == "bad.go" {
			http.Error(w, `{"error":{"message":"line not in diff"}}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%d,"content":{"raw":"ok"},"inline":{"path":%q,"to":%d}}`, 100+payload.Inline.To, payload.Inline.Path, payload.Inline.To)
	})
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader(`[{"path":"a.go","line":1,"body":"x"},{"path":"bad.go","line":2,"body":"y"}]`)

	var err error
	out := captureStdout(t, func() {
		err = runPR(&PRCmd{AddComments: &PRAddCommentsCmd{PRSelector: PRSelector{Number: "3"}, FromJSON: "-", JSON: true}}, bitbucketInfo)
	})
	if err == nil || exitCode(err) != exitError {
		t.Errorf("expected failure exit, got %v", err)
	}
	var results []addCommentResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(results) != 2 || !results[0].OK || results[0].Comment == nil || results[0].Comment.ID != 101 ||
		results[1].OK || !strings.Contains(results[1].Error, "line not in diff") {
		t.Errorf("results = %+v", results)
	}
}

func TestReplyAndAddCommentPayloads(t *testing.T) {
	var got map[string]any
	fakeBitbucket(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repositories/workspace/repo/pullrequests/3/comments" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":77,"content":{"raw":"done"},"parent":{"id":5},"user":{"display_name":"Me"}}`)
	})
	out := captureStdout(t, func() {
		if err := runPR(&PRCmd{Reply: &PRReplyCmd{PRSelector: PRSelector{Number: "3"}, CommentID: 5, Body: "done", JSON: true}}, bitbucketInfo); err != nil {
			t.Error(err)
		}
	})
	if want := map[string]any{"content": map[string]any{"raw": "done"}, "parent": map[string]any{"id": float64(5)}}; !reflect.DeepEqual(got, want) {
		t.Errorf("reply payload = %v", got)
	}
	var reply provider.PRComment
	if err := json.Unmarshal([]byte(out), &reply); err != nil || reply.ID != 77 || reply.ParentID != 5 || reply.Author != "Me" {
		t.Errorf("reply output = %s (%v)", out, err)
	}

	captureStdout(t, func() {
		if err := runPR(&PRCmd{AddComment: &PRAddCommentCmd{PRSelector: PRSelector{Number: "3"}, Location: "dir/a:b.go:12", Body: "hi"}}, bitbucketInfo); err != nil {
			t.Error(err)
		}
	})
	if want := map[string]any{"content": map[string]any{"raw": "hi"}, "inline": map[string]any{"path": "dir/a:b.go", "to": float64(12)}}; !reflect.DeepEqual(got, want) {
		t.Errorf("addcomment payload = %v", got)
	}
}

// TestCLIExitCodes runs the real main() against a temporary repository.
func TestCLIExitCodes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "feature/x"},
		{"remote", "add", "origin", "git@bitbucket.org:workspace/repo.git"},
		{"-c", "user.email=a@b", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repositories/workspace/repo/pullrequests":
			io.WriteString(w, `{"values":[]}`) // no PR for the current branch
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			io.WriteString(w, `{"values":[{"id":1,"state":"UNRESOLVED","content":{"raw":"t"}}]}`)
		default:
			http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
		}
	}))
	defer server.Close()

	run := func(args string) (stdout, stderr string, code int) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(),
			"GR_TEST_RUN_MAIN=1", "GR_TEST_ARGS=-C "+repo+" "+args,
			"BITBUCKET_API_BASE_URL="+server.URL, "BITBUCKET_TOKEN=t", "BITBUCKET_USERNAME=",
			"GR_CONFIG=", "HOME="+repo, "XDG_CONFIG_HOME="+repo)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		err := cmd.Run()
		code = 0
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return o.String(), e.String(), code
	}

	for _, tc := range []struct {
		args     string
		code     int
		jsonKind string // expected kind in JSON stderr ("" = text)
	}{
		{"pr tasks -n 1 --json", 0, ""},
		{"pr tasks --json", exitNotFound, "not_found"},     // no open PR for branch
		{"pr view 9999 --json", exitNotFound, "not_found"}, // API 404
		{"pr view 1 -n 2", exitUsage, ""},                  // conflicting selection
		{"pr addtask -n 1", exitUsage, ""},                 // missing body
		{"pr tasks -n https://bitbucket.org/x/y/pull-requests/1", exitUsage, ""},
		{"pr", exitUsage, ""},
	} {
		stdout, stderr, code := run(tc.args)
		if code != tc.code {
			t.Errorf("gr %s: exit %d, want %d\nstdout: %s\nstderr: %s", tc.args, code, tc.code, stdout, stderr)
			continue
		}
		if tc.jsonKind != "" {
			var e map[string]any
			if err := json.Unmarshal([]byte(stderr), &e); err != nil || e["kind"] != tc.jsonKind {
				t.Errorf("gr %s: stderr = %q, want JSON error of kind %s", tc.args, stderr, tc.jsonKind)
			}
			if stdout != "" {
				t.Errorf("gr %s: unexpected stdout %q", tc.args, stdout)
			}
		}
	}
}
