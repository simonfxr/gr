package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	project := gitlabProject(nfo)

	if opts.NoUpdatePRs {
		// Block if any open MRs use oldName as source branch.
		srcCount, err := countGitLabOpenMRs(gl, project, &oldName, nil)
		if err != nil {
			return err
		}
		if srcCount > 0 {
			return fmt.Errorf(
				"found %d open merge request(s) with source branch %q; GitLab does not support changing an MR's source branch. Close/merge or recreate them pointing to %q, then retry, or rerun with --no-update-prs/--force to proceed (will orphan MR source branches)",
				srcCount, oldName, newName,
			)
		}
	}

	// Ensure new branch exists
	if _, _, err := gl.Branches.CreateBranch(project, &gitlab.CreateBranchOptions{Branch: new(newName), Ref: new(oldName)}); err != nil {
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
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return err
	}

	// Find hash for old branch
	var branch struct {
		Target struct {
			Hash string `json:"hash"`
		} `json:"target"`
	}
	if _, err := bb.call(ctx, http.MethodGet, bitbucketBranchEndpoint(nfo, oldName)+"?fields=target.hash", nil, &branch); err != nil {
		return err
	}
	hash := branch.Target.Hash
	if hash == "" {
		hash = oldName
	}
	create := map[string]any{"name": newName, "target": map[string]any{"hash": hash}}
	if _, err := bb.call(ctx, http.MethodPost, bitbucketRepoEndpoint(nfo)+"/refs/branches", create, nil); err != nil {
		return err
	}

	// Retarget destination branch on open PRs and also source branch where applicable.
	if !opts.NoUpdatePRs {
		if err := bitbucketRetargetPRs(ctx, bb, nfo, "destination.branch.name = "+bitbucketQueryString(oldName), func(pr *bitbucketPR) {
			pr.Destination.Branch.Name = newName
		}); err != nil {
			return err
		}
		if err := bitbucketRetargetPRs(ctx, bb, nfo, "source.branch.name = "+bitbucketQueryString(oldName), func(pr *bitbucketPR) {
			pr.Source.Branch.Name = newName
		}); err != nil {
			return err
		}
	}

	_, err = bb.call(ctx, http.MethodDelete, bitbucketBranchEndpoint(nfo, oldName), nil, nil)
	return err
}

// bitbucketRetargetPRs updates the open PRs matching a query after applying
// mutate to their branches. The update repeats the PR's title, description,
// reviewers and flags so that they are preserved.
func bitbucketRetargetPRs(ctx context.Context, bb *bitbucketAPIClient, nfo *Info, query string, mutate func(*bitbucketPR)) error {
	prs, err := bitbucketOpenPRs(ctx, bb, nfo, query)
	if err != nil {
		return err
	}
	for _, listed := range prs {
		pr, err := bitbucketGetPR(ctx, bb, nfo, listed.ID)
		if err != nil {
			return err
		}
		mutate(pr)
		reviewers := []map[string]string{}
		for _, r := range pr.Reviewers {
			if r.UUID != "" {
				reviewers = append(reviewers, map[string]string{"uuid": r.UUID})
			}
		}
		source := map[string]any{"branch": map[string]any{"name": pr.Source.Branch.Name}}
		if pr.Source.Repository.FullName != "" {
			source["repository"] = map[string]any{"full_name": pr.Source.Repository.FullName}
		}
		payload := map[string]any{
			"title":               pr.Title,
			"description":         pr.Description,
			"close_source_branch": pr.CloseSourceBranch,
			"draft":               pr.Draft,
			"reviewers":           reviewers,
			"source":              source,
			"destination":         map[string]any{"branch": map[string]any{"name": pr.Destination.Branch.Name}},
		}
		if _, err := bb.call(ctx, http.MethodPut, bitbucketPREndpoint(nfo, pr.ID), payload, nil); err != nil {
			return err
		}
	}
	return nil
}

// countGitLabOpenMRs returns the number of open MRs for a project matching the provided
// source and/or target branch filters. A nil pointer means no filter for that field.
func countGitLabOpenMRs(gl *gitlab.Client, project string, sourceBranch, targetBranch *string) (int, error) {
	var page int64 = 1
	var perPage int64 = 100
	total := 0
	for {
		opts := &gitlab.ListProjectMergeRequestsOptions{
			PerPage: perPage, Page: page,
			State: new("opened"),
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

// retargetGitLabMRs recreates open MRs targeting oldTarget so they target newTarget.
//
// We avoid using UpdateMergeRequest for changing target branches because in practice
// it can be unreliable depending on project settings/permissions. Instead, we:
//  1. Create a new MR by cloning fields from the old one but using target=newTarget
//  2. Update the old MR's description to point to the new MR
//  3. Close the old MR
func retargetGitLabMRs(gl *gitlab.Client, project, oldTarget, newTarget string) error {
	var page int64 = 1
	var perPage int64 = 50
	for {
		listOpts := &gitlab.ListProjectMergeRequestsOptions{
			PerPage: perPage, Page: page,
			State:        new("opened"),
			TargetBranch: new(oldTarget),
		}
		mrs, resp, err := gl.MergeRequests.ListProjectMergeRequests(project, listOpts)
		if err != nil {
			return err
		}
		for _, mr := range mrs {
			// Step 1: Create a new MR cloning key fields but switching the target branch.
			newMR, _, err := gl.MergeRequests.CreateMergeRequest(project, &gitlab.CreateMergeRequestOptions{
				Title:        new(mr.Title),
				SourceBranch: new(mr.SourceBranch),
				TargetBranch: new(newTarget),
				Description:  new(mr.Description),
			})
			if err != nil {
				return fmt.Errorf("failed to create replacement MR for !%d: %w", mr.IID, err)
			}

			// Step 2: Update old MR description with superseded notice linking to new MR.
			link := newMR.WebURL
			notice := fmt.Sprintf("\n\nSuperseded by new MR here: %s", link)
			newDesc := mr.Description + notice
			if _, _, err := gl.MergeRequests.UpdateMergeRequest(project, mr.IID, &gitlab.UpdateMergeRequestOptions{Description: new(newDesc)}); err != nil {
				return fmt.Errorf("failed to update description for old MR !%d: %w", mr.IID, err)
			}

			// Step 3: Close the old MR.
			if _, _, err := gl.MergeRequests.UpdateMergeRequest(project, mr.IID, &gitlab.UpdateMergeRequestOptions{StateEvent: new("close")}); err != nil {
				return fmt.Errorf("failed to close old MR !%d: %w", mr.IID, err)
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
