package provider

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// bitbucketPR is the subset of a Bitbucket Cloud pull request that gr uses.
type bitbucketPR struct {
	ID                int            `json:"id"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	State             string         `json:"state"`
	Draft             bool           `json:"draft"`
	CloseSourceBranch bool           `json:"close_source_branch"`
	Author            *bitbucketUser `json:"author"`
	Reviewers         []struct {
		UUID string `json:"uuid"`
	} `json:"reviewers"`
	CreatedOn   string         `json:"created_on"`
	UpdatedOn   string         `json:"updated_on"`
	Source      bitbucketPRRef `json:"source"`
	Destination bitbucketPRRef `json:"destination"`
	Links       struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// bitbucketPRRef is the source or destination of a pull request.
type bitbucketPRRef struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (pr *bitbucketPR) toPRDetails() *PRDetails {
	d := &PRDetails{
		Number:        pr.ID,
		Title:         pr.Title,
		Body:          pr.Description,
		Author:        pr.Author.name(),
		State:         bitbucketState(pr.State),
		ProviderState: strings.ToLower(pr.State),
		Merged:        strings.EqualFold(pr.State, "MERGED"),
		Draft:         pr.Draft,
		Base:          pr.Destination.Branch.Name,
		Head:          pr.Source.Branch.Name,
		URL:           pr.Links.HTML.Href,
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339, pr.CreatedOn)
	d.UpdatedAt, _ = time.Parse(time.RFC3339, pr.UpdatedOn)
	return d
}

// bitbucketState normalizes a Bitbucket PR state (OPEN, MERGED, DECLINED,
// SUPERSEDED).
func bitbucketState(state string) string {
	switch strings.ToUpper(state) {
	case "OPEN":
		return StateOpen
	case "MERGED":
		return StateMerged
	default:
		return StateClosed
	}
}

// bitbucketGetPR fetches a single pull request.
func bitbucketGetPR(ctx context.Context, bb *bitbucketAPIClient, nfo *Info, number int) (*bitbucketPR, error) {
	var pr bitbucketPR
	if _, err := bb.call(ctx, http.MethodGet, bitbucketPREndpoint(nfo, number), nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// bitbucketOpenPRs lists the open pull requests matching a BBQL query.
func bitbucketOpenPRs(ctx context.Context, bb *bitbucketAPIClient, nfo *Info, query string) ([]bitbucketPR, error) {
	params := url.Values{"state": {"OPEN"}, "q": {query}, "pagelen": {strconv.Itoa(bitbucketPRMaxPageSize)}}
	var out []bitbucketPR
	err := bitbucketListPages(ctx, bb, bitbucketRepoEndpoint(nfo)+"/pullrequests", params, func(pr bitbucketPR) bool {
		out = append(out, pr)
		return true
	})
	return out, err
}

// DefaultBranchBitbucket returns the main branch of a Bitbucket Cloud repository.
func DefaultBranchBitbucket(ctx context.Context, bb *bitbucketAPIClient, nfo *Info) (string, error) {
	var repo struct {
		Mainbranch struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	}
	if _, err := bb.call(ctx, http.MethodGet, bitbucketRepoEndpoint(nfo)+"?fields=mainbranch.name", nil, &repo); err != nil {
		return "", err
	}
	if repo.Mainbranch.Name != "" {
		return repo.Mainbranch.Name, nil
	}
	return "main", nil
}
