package provider

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githublib "github.com/google/go-github/v74/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

type BranchListOptions struct {
	Pattern string
	Sort    string // name|date|author
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
	var out []BranchInfo
	for {
		branches, resp, err := gh.Repositories.ListBranches(ctx, nfo.Owner, nfo.Repo, listOpts)
		if err != nil {
			return nil, err
		}
		for _, b := range branches {
			name := b.GetName()
			info := BranchInfo{Name: name}
			if b.Commit != nil {
				sha := b.Commit.GetSHA()
				if sha != "" {
					// Fetch commit for date/author; ignore errors to keep listing responsive
					if gc, _, err := gh.Git.GetCommit(ctx, nfo.Owner, nfo.Repo, sha); err == nil && gc != nil && gc.Author != nil {
						if gc.Author.Date != nil {
							info.CommitDate = gc.Author.Date.Time
						}
						info.Author = gc.Author.GetName()
					}
				}
			}
			out = append(out, info)
		}
		if resp.NextPage == 0 {
			break
		}
		listOpts.ListOptions.Page = resp.NextPage
	}
	return sortBranches(out, opts), nil
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

type LocalBranchListOptions struct {
	Pattern  string
	Sort     string // name|date|author
	Merged   bool
	NoMerged bool
}

// ListLocalBranches lists local branches from the repository in cwd.
func ListLocalBranches(opts LocalBranchListOptions) ([]BranchInfo, error) {
	root, err := FindRepoRoot("")
	if err != nil {
		return nil, err
	}
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, err
	}
	headRef, err := repo.Head()
	if err != nil {
		return nil, err
	}
	headHash := headRef.Hash()

	var out []BranchInfo
	iter, err := repo.References()
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if !ref.Name().IsBranch() {
			return nil
		}
		name := ref.Name().Short()
		if p := strings.TrimSpace(opts.Pattern); p != "" {
			if ok, _ := path.Match(p, name); !ok {
				return nil
			}
		}
		// Determine merged state if requested
		if opts.Merged || opts.NoMerged {
			merged, _ := isAncestor(repo, ref.Hash(), headHash)
			if opts.Merged && !merged {
				return nil
			}
			if opts.NoMerged && merged {
				return nil
			}
		}
		// Fetch commit for date/author
		ci, err := repo.CommitObject(ref.Hash())
		var date time.Time
		author := ""
		if err == nil && ci != nil {
			date = ci.Author.When
			author = ci.Author.Name
			// keep object import
			_ = object.Commit{}
		}
		out = append(out, BranchInfo{Name: name, CommitDate: date, Author: author})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sortBranches(out, BranchListOptions{Pattern: "", Sort: opts.Sort}), nil
}

// isAncestor reports whether ancestor is reachable from descendant by walking parents.
func isAncestor(repo *git.Repository, ancestor, descendant plumbing.Hash) (bool, error) {
	if ancestor == descendant {
		return true, nil
	}
	seen := map[plumbing.Hash]bool{}
	queue := []plumbing.Hash{descendant}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if seen[h] {
			continue
		}
		seen[h] = true
		if h == ancestor {
			return true, nil
		}
		c, err := repo.CommitObject(h)
		if err != nil {
			continue
		}
		for _, p := range c.ParentHashes {
			if !seen[p] {
				queue = append(queue, p)
			}
		}
	}
	return false, nil
}
