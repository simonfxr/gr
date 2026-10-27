package main

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/simonfxr/gr/pkg/provider"
)

// PRSelector picks the pull request a command operates on. It is embedded in
// every command that targets a single PR.
type PRSelector struct {
	Number string `arg:"--number,-n" help:"pull request number or web URL [default: open PR of --branch]"`
	Branch string `arg:"--branch,-b" help:"use the open PR of this branch [default: current branch]"`
}

// prPathMarkers are the URL path segments preceding the PR number on
// Bitbucket (pull-requests), GitHub (pull) and GitLab (merge_requests).
var prPathMarkers = []string{"pull-requests", "pullrequests", "pull", "pulls", "merge_requests"}

// parsePRRef parses "123", "#123" or a PR web URL. For URLs the repository
// path is checked against the current repository so a URL of another
// repository never silently selects a PR with the same number here.
func parsePRRef(ref string, info *provider.Info) (int, error) {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.Atoi(strings.TrimPrefix(ref, "#")); err == nil {
		if n <= 0 {
			return 0, usageErrorf("invalid pull request number %q", ref)
		}
		return n, nil
	}
	if !strings.Contains(ref, "/") {
		return 0, usageErrorf("invalid pull request %q: expected a number or URL", ref)
	}
	raw := ref
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return 0, usageErrorf("invalid pull request URL %q: %v", ref, err)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	i := slices.IndexFunc(segs, func(s string) bool { return slices.Contains(prPathMarkers, s) })
	if i < 0 || i+1 >= len(segs) {
		return 0, usageErrorf("invalid pull request URL %q: no PR number found", ref)
	}
	n, err := strconv.Atoi(segs[i+1])
	if err != nil || n <= 0 {
		return 0, usageErrorf("invalid pull request URL %q: no PR number found", ref)
	}
	repoPath := segs[:i]
	if len(repoPath) > 0 && repoPath[len(repoPath)-1] == "-" { // GitLab: group/repo/-/merge_requests/N
		repoPath = repoPath[:len(repoPath)-1]
	}
	if len(repoPath) < 2 {
		return 0, usageErrorf("invalid pull request URL %q: no repository path", ref)
	}
	owner := strings.Join(repoPath[:len(repoPath)-1], "/")
	repo := repoPath[len(repoPath)-1]
	if info != nil && (!samePathComponent(owner, info.Owner) || !samePathComponent(repo, info.Repo)) {
		return 0, usageErrorf("pull request URL %q belongs to %s/%s, but the current repository is %s/%s",
			ref, owner, repo, info.Owner, info.Repo)
	}
	return n, nil
}

// samePathComponent compares URL and remote path components. Bitbucket URLs
// may use {uuid} placeholders instead of slugs; those cannot be checked offline.
func samePathComponent(fromURL, fromRemote string) bool {
	if strings.HasPrefix(fromURL, "{") && strings.HasSuffix(fromURL, "}") {
		return true
	}
	return strings.EqualFold(fromURL, fromRemote)
}

// resolvePR returns the PR number selected by an optional positional
// reference and the -n/-b selector, defaulting to the open PR of the
// current branch.
func resolvePR(ctx context.Context, info *provider.Info, positional string, sel PRSelector) (int, error) {
	var refs []string
	for _, r := range []string{positional, sel.Number} {
		if r = strings.TrimSpace(r); r != "" {
			refs = append(refs, r)
		}
	}
	branch := strings.TrimSpace(sel.Branch)
	if len(refs) > 0 {
		if branch != "" {
			return 0, usageErrorf("use either a pull request number/URL or --branch, not both")
		}
		number := 0
		for _, r := range refs {
			n, err := parsePRRef(r, info)
			if err != nil {
				return 0, err
			}
			if number != 0 && n != number {
				return 0, usageErrorf("conflicting pull requests %q and --number %q", positional, sel.Number)
			}
			number = n
		}
		return number, nil
	}
	if branch == "" {
		b, err := provider.CurrentBranch(info)
		if err != nil {
			return 0, usageErrorf("cannot determine current branch (%v); pass --number or --branch", err)
		}
		if branch = strings.TrimSpace(b); branch == "" {
			return 0, usageErrorf("cannot determine current branch; pass --number or --branch")
		}
	}
	rows, err := info.Provider.PrList(ctx, info, provider.ListOptions{State: "open", Head: branch, Limit: 5})
	if err != nil {
		return 0, err
	}
	switch len(rows) {
	case 0:
		return 0, notFoundErrorf("no open pull request found for branch %q", branch)
	case 1:
		return rows[0].Number, nil
	default:
		return 0, &cliError{
			kind:       kindAmbiguous,
			msg:        "found " + strconv.Itoa(len(rows)) + " open pull requests for branch " + strconv.Quote(branch) + "; pass --number",
			candidates: rows,
		}
	}
}
