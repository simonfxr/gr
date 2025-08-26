package provider

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	githublib "github.com/google/go-github/v74/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

type BranchListOptions struct {
	Pattern string
	Sort    string // name|date|author
	Author  string // case-insensitive substring match on author
	Since   string // human duration like 72h, 10d, 3w
}

type BranchInfo struct {
	Name       string
	CommitDate time.Time
	Author     string
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

func branchListRemoteGitHub(ctx context.Context, nfo *Info, opts BranchListOptions) ([]BranchInfo, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	listOpts := &githublib.BranchListOptions{ListOptions: githublib.ListOptions{PerPage: 100, Page: 1}}
	type stub struct {
		name string
		sha  string
	}
	var stubs []stub
	for {
		branches, resp, err := gh.Repositories.ListBranches(ctx, nfo.Owner, nfo.Repo, listOpts)
		if err != nil {
			return nil, err
		}
		for _, b := range branches {
			name := b.GetName()
			s := stub{name: name}
			if b.Commit != nil {
				s.sha = b.Commit.GetSHA()
			}
			stubs = append(stubs, s)
		}
		if resp.NextPage == 0 {
			break
		}
		listOpts.ListOptions.Page = resp.NextPage
	}
	// If a pattern is provided, pre-filter to avoid unnecessary API calls.
	if p := strings.TrimSpace(opts.Pattern); p != "" {
		filtered := make([]stub, 0, len(stubs))
		for _, s := range stubs {
			if ok, _ := path.Match(p, s.name); ok {
				filtered = append(filtered, s)
			}
		}
		stubs = filtered
	}

	// Fetch commit info in parallel; ignore individual errors to keep listing responsive.
	infos, _ := ParallelMapValues(stubs, func(s stub) (BranchInfo, error) {
		info := BranchInfo{Name: s.name}
		if s.sha == "" {
			return info, nil
		}
		if gc, _, err := gh.Git.GetCommit(ctx, nfo.Owner, nfo.Repo, s.sha); err == nil && gc != nil && gc.Author != nil {
			if gc.Author.Date != nil {
				info.CommitDate = gc.Author.Date.Time
			}
			info.Author = gc.Author.GetName()
		}
		return info, nil
	})

	return sortBranches(infos, opts), nil
}

func branchListRemoteGitLab(ctx context.Context, nfo *Info, opts BranchListOptions) ([]BranchInfo, error) {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return nil, err
	}
	project := fmt.Sprintf("%s/%s", nfo.Owner, nfo.Repo)
	page := 1
	perPage := 100
	var out []BranchInfo
	for {
		branches, resp, err := gl.Branches.ListBranches(project, &gitlab.ListBranchesOptions{ListOptions: gitlab.ListOptions{PerPage: perPage, Page: page}})
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
		if resp == nil || resp.NextPage == 0 || resp.CurrentPage >= resp.TotalPages {
			break
		}
		page = resp.NextPage
	}
	return sortBranches(out, opts), nil
}

func branchListRemoteBitbucket(ctx context.Context, nfo *Info, opts BranchListOptions) ([]BranchInfo, error) {
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return nil, err
	}
	res, err := bb.Repositories.Repository.ListBranches(&bitbucket.RepositoryBranchOptions{Owner: nfo.Owner, RepoSlug: nfo.Repo, Pagelen: 100})
	if err != nil {
		return nil, err
	}
	var out []BranchInfo
	if res != nil {
		for _, b := range res.Branches {
			info := BranchInfo{Name: b.Name}
			if b.Target != nil {
				if dt, ok := b.Target["date"].(string); ok {
					if t, err := time.Parse(time.RFC3339, dt); err == nil {
						info.CommitDate = t
					}
				}
				if au, ok := b.Target["author"].(map[string]any); ok {
					if user, ok := au["user"].(map[string]any); ok {
						if name, ok := user["display_name"].(string); ok {
							info.Author = name
						}
					}
				}
			}
			out = append(out, info)
		}
	}
	return sortBranches(out, opts), nil
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
