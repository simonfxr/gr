package provider

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

type BranchRenameOptions struct {
	// NoUpdatePRs leaves open PRs targeting the branch alone.
	NoUpdatePRs bool
	// Force renames even if open GitLab MRs use the branch as source, which
	// GitLab cannot change: those MRs lose their source branch.
	Force bool
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
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return err
	}
	project := gitlabProject(nfo)

	// Check before changing anything.
	proj, _, err := gl.Projects.GetProject(project, nil)
	if err != nil {
		return err
	}
	if proj.DefaultBranch == oldName {
		return invalidArgf("%q is the default branch of %s; change the default branch first", oldName, project)
	}
	// GitLab cannot change an MR's source branch.
	if !opts.Force {
		fromOld, err := gitlabMRsFromBranch(gl, project, proj, oldName)
		if err != nil {
			return err
		}
		if len(fromOld) > 0 {
			example := cmp.Or(fromOld[0].WebURL, fmt.Sprintf("!%d", fromOld[0].IID))
			return invalidArgf("%d open merge request(s) use %q as source branch (e.g. %s), and GitLab cannot change an MR's source branch; "+
				"merge or close them first, or rerun with --force (they lose their source branch and need to be recreated from %q)",
				len(fromOld), oldName, example, newName)
		}
	}

	// Ensure new branch exists
	if _, _, err := gl.Branches.CreateBranch(project, &gitlab.CreateBranchOptions{Branch: new(newName), Ref: new(oldName)}); err != nil {
		if !containsIgnoreCase(err.Error(), "already exists") {
			return err
		}
	}

	if !opts.NoUpdatePRs {
		if err := retargetGitLabMRs(gl, project, oldName, newName); err != nil {
			return err
		}
	}

	_, err = gl.Branches.DeleteBranch(project, oldName)
	return err
}

func branchRenameBitbucket(ctx context.Context, nfo *Info, oldName, newName string, opts BranchRenameOptions) error {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return err
	}
	def, err := DefaultBranchBitbucket(ctx, bb, nfo)
	if err != nil {
		return err
	}
	if def == oldName {
		return invalidArgf("%q is the main branch of %s/%s; change the main branch first", oldName, nfo.Owner, nfo.Repo)
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
		if err := bitbucketRetargetPRs(ctx, bb, nfo, "destination.branch.name = "+bitbucketQueryString(oldName), func(pr *bitbucketPR) bool {
			pr.Destination.Branch.Name = newName
			return true
		}); err != nil {
			return err
		}
		fullName := nfo.Owner + "/" + nfo.Repo
		if err := bitbucketRetargetPRs(ctx, bb, nfo, "source.branch.name = "+bitbucketQueryString(oldName), func(pr *bitbucketPR) bool {
			// PRs from forks come from a same-named branch of the fork.
			if src := pr.Source.Repository.FullName; src != "" && !strings.EqualFold(src, fullName) {
				return false
			}
			pr.Source.Branch.Name = newName
			return true
		}); err != nil {
			return err
		}
	}

	_, err = bb.call(ctx, http.MethodDelete, bitbucketBranchEndpoint(nfo, oldName), nil, nil)
	return err
}

// bitbucketRetargetPRs updates the open PRs matching a query after applying
// mutate to their branches; PRs for which mutate returns false are skipped.
// The update repeats the PR's title, description, reviewers and flags so
// that they are preserved.
func bitbucketRetargetPRs(ctx context.Context, bb *bitbucketAPIClient, nfo *Info, query string, mutate func(*bitbucketPR) bool) error {
	prs, err := bitbucketOpenPRs(ctx, bb, nfo, query)
	if err != nil {
		return err
	}
	for _, listed := range prs {
		pr, err := bitbucketGetPR(ctx, bb, nfo, listed.ID)
		if err != nil {
			return err
		}
		if !mutate(pr) {
			continue
		}
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

// gitlabMRsFromBranch returns the open MRs whose source is branch of proj
// (at path): MRs within proj and, for a fork, MRs into the project it was
// forked from. MRs into proj from forks with a same-named branch do not count.
func gitlabMRsFromBranch(gl *gitlab.Client, path string, proj *gitlab.Project, branch string) ([]*gitlab.BasicMergeRequest, error) {
	targets := []any{path}
	if proj.ForkedFromProject != nil && proj.ForkedFromProject.ID != 0 {
		targets = append(targets, proj.ForkedFromProject.ID)
	}
	var out []*gitlab.BasicMergeRequest
	for _, target := range targets {
		mrs, err := gitlabOpenMRs(gl, target, &branch, nil)
		if err != nil {
			return nil, err
		}
		for _, mr := range mrs {
			if mr.SourceProjectID == 0 || mr.SourceProjectID == proj.ID {
				out = append(out, mr)
			}
		}
	}
	return out, nil
}

// gitlabOpenMRs lists the open MRs of a project with the given source
// and/or target branch (nil: any).
func gitlabOpenMRs(gl *gitlab.Client, project any, sourceBranch, targetBranch *string) ([]*gitlab.BasicMergeRequest, error) {
	opts := &gitlab.ListProjectMergeRequestsOptions{
		ListOptions:  gitlab.ListOptions{PerPage: 100, Page: 1},
		State:        new("opened"),
		SourceBranch: sourceBranch,
		TargetBranch: targetBranch,
	}
	var out []*gitlab.BasicMergeRequest
	for {
		mrs, resp, err := gl.MergeRequests.ListProjectMergeRequests(project, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, mrs...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// retargetGitLabMRs changes the target branch of the open MRs targeting
// oldTarget, which keeps their discussions and approvals. If GitLab rejects
// the update, the MR is recreated against newTarget instead: the new MR
// copies title and description, and the old one is closed with a link.
func retargetGitLabMRs(gl *gitlab.Client, project, oldTarget, newTarget string) error {
	// Collect first: retargeting removes MRs from the filtered listing.
	mrs, err := gitlabOpenMRs(gl, project, nil, &oldTarget)
	if err != nil {
		return err
	}
	for _, mr := range mrs {
		_, _, updateErr := gl.MergeRequests.UpdateMergeRequest(project, mr.IID, &gitlab.UpdateMergeRequestOptions{TargetBranch: new(newTarget)})
		if updateErr == nil {
			continue
		}
		if err := recreateGitLabMR(gl, project, mr, newTarget); err != nil {
			return fmt.Errorf("retargeting !%d failed (%v), and so did recreating it: %w", mr.IID, updateErr, err)
		}
	}
	return nil
}

func recreateGitLabMR(gl *gitlab.Client, project string, mr *gitlab.BasicMergeRequest, newTarget string) error {
	if mr.SourceProjectID != mr.TargetProjectID {
		// Created here, the new MR would use a same-named branch of this
		// project instead of the fork's.
		return fmt.Errorf("it comes from a fork, which gr does not recreate MRs for")
	}
	newMR, _, err := gl.MergeRequests.CreateMergeRequest(project, &gitlab.CreateMergeRequestOptions{
		Title:        new(mr.Title),
		SourceBranch: new(mr.SourceBranch),
		TargetBranch: new(newTarget),
		Description:  new(mr.Description),
	})
	if err != nil {
		return err
	}
	desc := mr.Description + "\n\nSuperseded by " + newMR.WebURL
	if _, _, err := gl.MergeRequests.UpdateMergeRequest(project, mr.IID, &gitlab.UpdateMergeRequestOptions{
		Description: new(desc), StateEvent: new("close"),
	}); err != nil {
		return fmt.Errorf("closing !%d after recreating it as !%d: %w", mr.IID, newMR.IID, err)
	}
	return nil
}

// containsIgnoreCase reports whether substr is within s, case-insensitive.
func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
