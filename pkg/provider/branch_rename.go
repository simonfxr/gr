package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	bitbucket "github.com/ktrysmt/go-bitbucket"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

type BranchRenameOptions struct {
	NoUpdatePRs bool
}

// BranchRename renames a remote branch across providers.
func (p Provider) BranchRename(ctx context.Context, nfo *Info, oldName, newName string, opts BranchRenameOptions) error {
	if nfo == nil {
		return errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return branchRenameGitHub(ctx, nfo, oldName, newName, opts)
	case ProviderGitLab:
		return branchRenameGitLab(ctx, nfo, oldName, newName, opts)
	case ProviderBitbucket:
		return branchRenameBitbucket(ctx, nfo, oldName, newName, opts)
	default:
		return fmt.Errorf("unknown provider: %v", p)
	}
}

// Provider-specific implementations

func branchRenameGitHub(ctx context.Context, nfo *Info, oldName, newName string, _ BranchRenameOptions) error {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return err
	}
	_, _, err = gh.Repositories.RenameBranch(ctx, nfo.Owner, nfo.Repo, oldName, newName)
	return err
}

func branchRenameGitLab(ctx context.Context, nfo *Info, oldName, newName string, opts BranchRenameOptions) error {
	gl, err := gitlabClient(nfo)
	if err != nil {
		return err
	}
	project := fmt.Sprintf("%s/%s", nfo.Owner, nfo.Repo)

	if !opts.NoUpdatePRs {
		// Block if any open MRs use oldName as source branch.
		srcCount, err := countGitLabOpenMRs(gl, project, &oldName, nil)
		if err != nil {
			return err
		}
		if srcCount > 0 {
			return fmt.Errorf("found %d open merge request(s) with source branch %q; cannot update MR source branch on GitLab. Close/merge or recreate them pointing to %q, then retry", srcCount, oldName, newName)
		}
	}

	// Ensure new branch exists
	if _, _, err := gl.Branches.CreateBranch(project, &gitlab.CreateBranchOptions{Branch: gitlab.Ptr(newName), Ref: gitlab.Ptr(oldName)}); err != nil {
		if err.Error() == "" || (!containsIgnoreCase(err.Error(), "already exists") && !containsIgnoreCase(err.Error(), "Branch already exists")) {
			return err
		}
	}

	// Retarget MRs with target == oldName
	if !opts.NoUpdatePRs {
		if err := retargetGitLabMRs(gl, project, oldName, newName); err != nil {
			return err
		}
	}

	// Delete old branch
	if _, err := gl.Branches.DeleteBranch(project, oldName); err != nil {
		return err
	}
	return nil
}

func branchRenameBitbucket(ctx context.Context, nfo *Info, oldName, newName string, opts BranchRenameOptions) error {
	bb, err := bitbucketClient(nfo)
	if err != nil {
		return err
	}

	// Note: We will update PRs safely by preserving fields fetched from API.

	// Find hash for old branch
	b, err := bb.Repositories.Repository.GetBranch(&bitbucket.RepositoryBranchOptions{Owner: nfo.Owner, RepoSlug: nfo.Repo, BranchName: oldName})
	if err != nil {
		return err
	}
	hash := ""
	if b != nil && b.Target != nil {
		if h, ok := b.Target["hash"].(string); ok {
			hash = h
		}
	}
	if hash == "" {
		hash = oldName
	}
	if _, err := bb.Repositories.Repository.CreateBranch(&bitbucket.RepositoryBranchCreationOptions{Owner: nfo.Owner, RepoSlug: nfo.Repo, Name: newName, Target: bitbucket.RepositoryBranchTarget{Hash: hash}}); err != nil {
		return err
	}

	// Retarget destination branch on open PRs and also source branch where applicable.
	if !opts.NoUpdatePRs {
		// Destination retargeting
		if err := bitbucketRetargetPRs(bb, nfo.Owner, nfo.Repo, fmt.Sprintf("destination.branch.name = \"%s\"", oldName), func(po *bitbucket.PullRequestsOptions) {
			po.DestinationBranch = newName
		}); err != nil {
			return err
		}
		// Source retargeting
		if err := bitbucketRetargetPRs(bb, nfo.Owner, nfo.Repo, fmt.Sprintf("source.branch.name = \"%s\"", oldName), func(po *bitbucket.PullRequestsOptions) {
			po.SourceBranch = newName
		}); err != nil {
			return err
		}
	}

	if err := bb.Repositories.Repository.DeleteBranch(&bitbucket.RepositoryBranchDeleteOptions{Owner: nfo.Owner, RepoSlug: nfo.Repo, RefName: oldName}); err != nil {
		return err
	}
	return nil
}

// bitbucketRetargetPRs finds open PRs matching a query and updates them by preserving existing
// fields while applying the provided mutate function to the update options (e.g., change source/destination branch).
func bitbucketRetargetPRs(bb *bitbucket.Client, owner, repo, query string, mutate func(*bitbucket.PullRequestsOptions)) error {
	res, err := bb.Repositories.PullRequests.Gets(&bitbucket.PullRequestsOptions{
		Owner:    owner,
		RepoSlug: repo,
		States:   []string{"OPEN"},
		Query:    query,
	})
	if err != nil {
		return err
	}
	m, ok := res.(map[string]any)
	if !ok {
		return nil
	}
	vals, _ := m["values"].([]interface{})
	for _, it := range vals {
		pr, _ := it.(map[string]any)
		if pr == nil {
			continue
		}
		// Fetch full PR to preserve fields
		idStr := ""
		if v, ok := pr["id"].(float64); ok {
			idStr = fmt.Sprintf("%d", int(v))
		}
		if idStr == "" {
			continue
		}
		full, err := bb.Repositories.PullRequests.Get(&bitbucket.PullRequestsOptions{Owner: owner, RepoSlug: repo, ID: idStr})
		if err != nil {
			return err
		}
		fp, ok := full.(map[string]any)
		if !ok {
			continue
		}
		// Extract and preserve fields
		title, _ := fp["title"].(string)
		description, _ := fp["description"].(string)
		closeSource := false
		if b, ok := fp["close_source_branch"].(bool); ok {
			closeSource = b
		}
		draft := false
		if b, ok := fp["draft"].(bool); ok {
			draft = b
		}
		// Destination branch
		destBranch := ""
		if dst, ok := fp["destination"].(map[string]any); ok {
			if br, ok := dst["branch"].(map[string]any); ok {
				if name, ok := br["name"].(string); ok {
					destBranch = name
				}
			}
		}
		// Source repo full name (for forks)
		sourceRepo := ""
		if src, ok := fp["source"].(map[string]any); ok {
			if r, ok := src["repository"].(map[string]any); ok {
				if fn, ok := r["full_name"].(string); ok {
					sourceRepo = fn
				}
			}
		}
		// Reviewers UUIDs
		reviewers := []string{}
		if rvs, ok := fp["reviewers"].([]interface{}); ok {
			for _, rv := range rvs {
				if m, ok := rv.(map[string]any); ok {
					if uuid, ok := m["uuid"].(string); ok && uuid != "" {
						reviewers = append(reviewers, uuid)
					}
				}
			}
		}

		// Prepare update options preserving fields
		po := &bitbucket.PullRequestsOptions{
			Owner:             owner,
			RepoSlug:          repo,
			ID:                idStr,
			Title:             title,
			Description:       description,
			CloseSourceBranch: closeSource,
			Reviewers:         reviewers,
			Draft:             draft,
			DestinationBranch: destBranch,
			SourceRepository:  sourceRepo,
		}
		// Apply mutation (change destination or source branch)
		mutate(po)

		if _, err := bb.Repositories.PullRequests.Update(po); err != nil {
			return err
		}
	}
	return nil
}

// countGitLabOpenMRs returns the number of open MRs for a project matching the provided
// source and/or target branch filters. A nil pointer means no filter for that field.
func countGitLabOpenMRs(gl *gitlab.Client, project string, sourceBranch, targetBranch *string) (int, error) {
	page := 1
	perPage := 100
	total := 0
	for {
		opts := &gitlab.ListProjectMergeRequestsOptions{
			ListOptions: gitlab.ListOptions{PerPage: perPage, Page: page},
			State:       gitlab.Ptr("opened"),
		}
		if sourceBranch != nil {
			opts.SourceBranch = sourceBranch
		}
		if targetBranch != nil {
			opts.TargetBranch = targetBranch
		}
		mrs, resp, err := gl.MergeRequests.ListProjectMergeRequests(project, opts)
		if err != nil {
			return 0, err
		}
		total += len(mrs)
		if resp == nil || resp.NextPage == 0 || resp.CurrentPage >= resp.TotalPages {
			break
		}
		page = resp.NextPage
	}
	return total, nil
}

// retargetGitLabMRs updates open MRs targeting oldTarget to point to newTarget.
func retargetGitLabMRs(gl *gitlab.Client, project, oldTarget, newTarget string) error {
	page := 1
	perPage := 50
	for {
		listOpts := &gitlab.ListProjectMergeRequestsOptions{
			ListOptions:  gitlab.ListOptions{PerPage: perPage, Page: page},
			State:        gitlab.Ptr("opened"),
			TargetBranch: gitlab.Ptr(oldTarget),
		}
		mrs, resp, err := gl.MergeRequests.ListProjectMergeRequests(project, listOpts)
		if err != nil {
			return err
		}
		for _, mr := range mrs {
			_, _, err := gl.MergeRequests.UpdateMergeRequest(project, mr.IID, &gitlab.UpdateMergeRequestOptions{TargetBranch: gitlab.Ptr(newTarget)})
			if err != nil {
				return err
			}
		}
		if resp == nil || resp.NextPage == 0 || resp.CurrentPage >= resp.TotalPages {
			break
		}
		page = resp.NextPage
	}
	return nil
}

// containsIgnoreCase reports whether substr is within s, case-insensitive.
func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
