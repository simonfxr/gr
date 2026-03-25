package provider

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// BranchWebURL constructs a web URL for viewing a branch for the given repo info.
// It supports GitHub, GitLab, and Bitbucket Cloud. For self-hosted instances,
// it uses Info.HTTPBase if available; otherwise it falls back to https://<host>.
func (p Provider) BranchWebURL(nfo *Info, branch string) (string, error) {
	if nfo == nil {
		return "", errors.New("missing repo info")
	}
	b := strings.TrimSpace(branch)
	if b == "" {
		return "", errors.New("branch name is required")
	}
	webBase := strings.TrimRight(nfo.HTTPBase, "/")
	if webBase == "" {
		switch p {
		case ProviderGitHub:
			webBase = "https://github.com"
		case ProviderGitLab:
			webBase = "https://gitlab.com"
		case ProviderBitbucket:
			webBase = "https://bitbucket.org"
		default:
			webBase = "https://" + strings.TrimSpace(nfo.Host)
		}
	}

	// Escape components
	ownerEsc := strings.Trim(strings.TrimSpace(nfo.Owner), "/")
	repoEsc := strings.Trim(strings.TrimSpace(nfo.Repo), "/")
	// owner may include slashes (gitlab groups). Escape each segment.
	var escOwnerSegs []string
	for seg := range strings.SplitSeq(ownerEsc, "/") {
		if seg == "" {
			continue
		}
		escOwnerSegs = append(escOwnerSegs, url.PathEscape(seg))
	}
	ownerPath := strings.Join(escOwnerSegs, "/")
	repoPath := url.PathEscape(repoEsc)
	branchPath := url.PathEscape(b)

	switch p {
	case ProviderGitHub:
		// https://host/owner/repo/tree/branch
		return fmt.Sprintf("%s/%s/%s/tree/%s", webBase, ownerPath, repoPath, branchPath), nil
	case ProviderGitLab:
		// https://host/owner/repo/-/tree/branch
		return fmt.Sprintf("%s/%s/%s/-/tree/%s", webBase, ownerPath, repoPath, branchPath), nil
	case ProviderBitbucket:
		if strings.EqualFold(nfo.Host, "bitbucket.org") || strings.EqualFold(nfo.Variant, "cloud") || strings.Contains(webBase, "bitbucket.org") {
			// Bitbucket Cloud: show repo tree at branch
			// e.g., https://bitbucket.org/owner/repo/src/branch/
			return fmt.Sprintf("%s/%s/%s/src/%s/", webBase, ownerPath, repoPath, branchPath), nil
		}
		// Bitbucket Server (best-effort): https://host/projects/OWNER/repos/REPO/browse?at=refs/heads/branch
		return fmt.Sprintf("%s/projects/%s/repos/%s/browse?at=refs/heads/%s", webBase, url.PathEscape(nfo.Owner), repoPath, url.QueryEscape(b)), nil
	default:
		return "", fmt.Errorf("unsupported provider for branch URL: %v", p)
	}
}
