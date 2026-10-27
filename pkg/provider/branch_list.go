package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

type BranchListOptions struct {
	Pattern string
	Sort    string // name|date|author
	Author  string // case-insensitive substring match on author
	Since   string // human duration like 72h, 10d, 3w
}

type BranchInfo struct {
	Name       string    `json:"name"`
	CommitDate time.Time `json:"commit_date"`
	Author     string    `json:"author"`
}

// BranchListRemote lists remote branches using provider APIs.
func (p Provider) BranchListRemote(ctx context.Context, nfo *Info, opts BranchListOptions) ([]BranchInfo, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return branchListRemoteGitHub(ctx, nfo, opts)
	case ProviderGitLab:
		return branchListRemoteGitLab(ctx, nfo, opts)
	case ProviderBitbucket:
		return branchListRemoteBitbucket(ctx, nfo, opts)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

// branchListRemoteGitHub lists branches with their head commit in one
// GraphQL request per 100 branches (REST needs one request per branch).
func branchListRemoteGitHub(ctx context.Context, nfo *Info, opts BranchListOptions) ([]BranchInfo, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	const query = `query($owner: String!, $repo: String!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    refs(refPrefix: "refs/heads/", first: 100, after: $cursor) {
      pageInfo { hasNextPage endCursor }
      nodes { name target { ... on Commit { author { name date } } } }
    }
  }
}`
	vars := map[string]any{"owner": nfo.Owner, "repo": nfo.Repo, "cursor": nil}
	var out []BranchInfo
	for {
		var res struct {
			Repository *struct {
				Refs struct {
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
					Nodes []struct {
						Name   string
						Target struct {
							Author *struct {
								Name string
								Date time.Time
							}
						}
					}
				}
			}
		}
		if err := githubGraphQL(ctx, gh, query, vars, &res); err != nil {
			return nil, err
		}
		if res.Repository == nil {
			return nil, notFoundf("repository %s/%s not found", nfo.Owner, nfo.Repo)
		}
		for _, n := range res.Repository.Refs.Nodes {
			b := BranchInfo{Name: n.Name}
			if a := n.Target.Author; a != nil {
				b.Author, b.CommitDate = a.Name, a.Date
			}
			out = append(out, b)
		}
		if !res.Repository.Refs.PageInfo.HasNextPage {
			return sortBranches(out, opts), nil
		}
		vars["cursor"] = res.Repository.Refs.PageInfo.EndCursor
	}
}

func branchListRemoteGitLab(ctx context.Context, nfo *Info, opts BranchListOptions) ([]BranchInfo, error) {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	project := gitlabProject(nfo)
	var page int64 = 1
	var perPage int64 = 100
	var out []BranchInfo
	for {
		branches, resp, err := gl.Branches.ListBranches(project, &gitlab.ListBranchesOptions{PerPage: perPage, Page: page})
		if err != nil {
			return nil, err
		}
		for _, b := range branches {
			info := BranchInfo{Name: b.Name}
			if b.Commit != nil {
				info.Author = b.Commit.AuthorName
				if b.Commit.CommittedDate != nil {
					info.CommitDate = *b.Commit.CommittedDate
				}
			}
			out = append(out, info)
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}
	return sortBranches(out, opts), nil
}

func branchListRemoteBitbucket(ctx context.Context, nfo *Info, opts BranchListOptions) ([]BranchInfo, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	params := url.Values{
		"pagelen": {"100"},
		"fields":  {"next,values.name,values.target.date,values.target.author.user.display_name"},
	}
	nameQuery := bitbucketBranchNameQuery(opts.Pattern)
	var filters []string
	if nameQuery != "" {
		filters = append(filters, nameQuery)
	}
	if author := strings.TrimSpace(opts.Author); author != "" {
		filters = append(filters, "target.author.user.display_name ~ "+bitbucketQueryString(author))
	}
	if age, err := parseMaxAge(opts.Since); err == nil && age > 0 {
		// Truncating down to seconds only broadens this prefilter. The local
		// cutoff remains authoritative, including subsecond precision.
		cutoff := time.Now().Add(-age).UTC().Format(time.RFC3339)
		// BBQL datetime literals are unquoted, unlike string literals.
		filters = append(filters, "target.date > "+cutoff)
	}
	query := strings.Join(filters, " AND ")
	if query != "" {
		params.Set("q", query)
	}
	endpoint := bitbucketRepoEndpoint(nfo) + "/refs/branches"
	var out []BranchInfo
	visit := func(b bitbucketBranchListItem) bool {
		info := BranchInfo{Name: b.Name, Author: b.Target.Author.User.DisplayName}
		if t, err := time.Parse(time.RFC3339, b.Target.Date); err == nil {
			info.CommitDate = t
		}
		out = append(out, info)
		return true
	}
	err = bitbucketListPages(ctx, bb, endpoint, params, visit)
	// Branch name filtering is documented, but support for nested fields can
	// vary. Retry a rejected query with only that known-safe prefilter; all
	// requested filters are enforced locally in either case.
	var apiErr *bitbucketAPIError
	if query != nameQuery && errors.As(err, &apiErr) && apiErr.status == http.StatusBadRequest {
		out = nil
		params.Del("q")
		if nameQuery != "" {
			params.Set("q", nameQuery)
		}
		err = bitbucketListPages(ctx, bb, endpoint, params, visit)
	}
	if err != nil {
		return nil, err
	}
	return sortBranches(out, opts), nil
}

type bitbucketBranchListItem struct {
	Name   string `json:"name"`
	Target struct {
		Date   string `json:"date"`
		Author struct {
			User struct {
				DisplayName string `json:"display_name"`
			} `json:"user"`
		} `json:"author"`
	} `json:"target"`
}

// bitbucketBranchNameQuery only supplies a necessary condition for path.Match.
// Wildcards and character classes cannot be translated to Bitbucket's query
// language; every match must nevertheless contain each unescaped literal run.
func bitbucketBranchNameQuery(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ""
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return ""
	}
	var literal strings.Builder
	longest := ""
	flush := func() {
		if literal.Len() > len(longest) {
			longest = literal.String()
		}
		literal.Reset()
	}
	runes := []rune(pattern)
	glob, inClass := false, false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' {
			i++
			if !inClass {
				literal.WriteRune(runes[i])
			}
			continue
		}
		if inClass {
			if r == ']' {
				inClass = false
			}
			continue
		}
		switch r {
		case '*', '?', '[':
			glob = true
			flush()
			inClass = r == '['
		default:
			literal.WriteRune(r)
		}
	}
	if !glob {
		return "name = " + bitbucketQueryString(literal.String())
	}
	flush()
	if longest == "" {
		return ""
	}
	return "name ~ " + bitbucketQueryString(longest)
}

func sortBranches(in []BranchInfo, opts BranchListOptions) []BranchInfo {
	// Filter pattern
	if p := strings.TrimSpace(opts.Pattern); p != "" {
		// Convert glob to a simple HasPrefix/Contains if needed; use path.Match for unix-style globs
		filtered := make([]BranchInfo, 0, len(in))
		for _, b := range in {
			if ok, _ := path.Match(p, b.Name); ok {
				filtered = append(filtered, b)
			}
		}
		in = filtered
	}
	// Filter by author substring (case-insensitive)
	if a := strings.ToLower(strings.TrimSpace(opts.Author)); a != "" {
		filtered := make([]BranchInfo, 0, len(in))
		for _, b := range in {
			if strings.Contains(strings.ToLower(b.Author), a) {
				filtered = append(filtered, b)
			}
		}
		in = filtered
	}
	// Filter by max age since last commit
	if s := strings.TrimSpace(opts.Since); s != "" {
		if d, err := parseMaxAge(s); err == nil && d > 0 {
			cutoff := time.Now().Add(-d)
			filtered := make([]BranchInfo, 0, len(in))
			for _, b := range in {
				if !b.CommitDate.IsZero() && b.CommitDate.After(cutoff) {
					filtered = append(filtered, b)
				}
			}
			in = filtered
		}
	}
	sortKey := strings.ToLower(strings.TrimSpace(opts.Sort))
	switch sortKey {
	case "date":
		sort.SliceStable(in, func(i, j int) bool { return in[i].CommitDate.After(in[j].CommitDate) })
	case "author":
		sort.SliceStable(in, func(i, j int) bool { return strings.ToLower(in[i].Author) < strings.ToLower(in[j].Author) })
	default:
		sort.SliceStable(in, func(i, j int) bool { return strings.ToLower(in[i].Name) < strings.ToLower(in[j].Name) })
	}
	return in
}

// parseMaxAge parses human durations like "72h", "10d", "3w", or composites like "1w2d3h45m".
// It supports Go's time.ParseDuration formats plus 'd' (days) and 'w' (weeks), and allows combining units.
// Only integer values are supported for 'd' and 'w' parts.
func parseMaxAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	// Try native parser first (supports sequences like 1h30m, but not d/w)
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Normalize and parse composite with additional units 'd' and 'w'
	// Accept lowercase only
	s = strings.ToLower(s)
	// Allow microseconds in either 'us' or 'µs'
	// We'll scan number+unit pairs repeatedly.
	i := 0
	total := time.Duration(0)
	for i < len(s) {
		// skip spaces
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		// parse integer value
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("invalid duration at %q", s[i:])
		}
		val := int64(0)
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			val = val*10 + int64(s[i]-'0')
			i++
		}
		if i >= len(s) {
			return 0, fmt.Errorf("missing unit in duration: %q", s)
		}
		// detect unit (prefer two-letter units)
		unit := ""
		if i+2 <= len(s) {
			u2 := s[i : i+2]
			switch u2 {
			case "ns", "us", "µs", "ms":
				unit = u2
				i += 2
			}
		}
		if unit == "" {
			// single-letter unit
			u1 := s[i]
			switch u1 {
			case 's', 'm', 'h', 'd', 'w':
				unit = string(u1)
				i++
			default:
				return 0, fmt.Errorf("unsupported duration unit at %q", s[i:])
			}
		}
		mult := time.Duration(0)
		switch unit {
		case "ns":
			mult = time.Nanosecond
		case "us", "µs":
			mult = time.Microsecond
		case "ms":
			mult = time.Millisecond
		case "s":
			mult = time.Second
		case "m":
			mult = time.Minute
		case "h":
			mult = time.Hour
		case "d":
			mult = 24 * time.Hour
		case "w":
			mult = 7 * 24 * time.Hour
		default:
			return 0, fmt.Errorf("unsupported duration unit: %q", unit)
		}
		total += time.Duration(val) * mult
	}
	return total, nil
}
