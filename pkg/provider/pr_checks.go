package provider

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// Check states, the same for all providers. A PR's overall checks state is
// CheckPass, CheckFail, CheckPending or ChecksNone.
const (
	CheckPass      = "pass"
	CheckFail      = "fail"
	CheckPending   = "pending" // queued or running
	CheckSkipped   = "skipped" // also neutral results and manual GitLab jobs
	CheckCancelled = "cancelled"
	ChecksNone     = "none" // no checks reported
)

// Check is one CI result of a PR: a GitHub check run or commit status, a
// GitLab pipeline job, or a Bitbucket build status.
type Check struct {
	Name string `json:"name"`
	// Group is the GitLab pipeline stage.
	Group string `json:"group,omitempty"`
	// State is CheckPass, CheckFail, CheckPending, CheckSkipped or
	// CheckCancelled.
	State string `json:"state"`
	// ProviderState is the provider's own status, lowercased, e.g. GitHub
	// "timed_out" or GitLab "manual".
	ProviderState string `json:"provider_state"`
	// Optional is set for checks allowed to fail (GitLab allow_failure);
	// their failure does not fail the PR's checks.
	Optional    bool      `json:"optional,omitempty"`
	Description string    `json:"description,omitempty"`
	URL         string    `json:"url,omitempty"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
}

// ChecksReport lists the checks of a PR's head commit.
type ChecksReport struct {
	PR     int    `json:"pr"`
	Commit string `json:"commit,omitempty"`
	// State is CheckFail if a non-optional check failed or was cancelled,
	// else CheckPending while any check is pending, else CheckPass;
	// ChecksNone without checks.
	State string `json:"state"`
	// URL is the GitLab pipeline.
	URL    string  `json:"url,omitempty"`
	Checks []Check `json:"checks"`
}

// Pending returns the number of pending checks.
func (r *ChecksReport) Pending() int {
	n := 0
	for _, c := range r.Checks {
		if c.State == CheckPending {
			n++
		}
	}
	return n
}

// finish sorts the checks (failures first, then by name) and sets State.
func (r *ChecksReport) finish() *ChecksReport {
	order := map[string]int{CheckFail: 0, CheckCancelled: 1, CheckPending: 2, CheckPass: 3, CheckSkipped: 4}
	slices.SortStableFunc(r.Checks, func(a, b Check) int {
		return cmp.Or(cmp.Compare(order[a.State], order[b.State]), cmp.Compare(a.Group, b.Group), cmp.Compare(a.Name, b.Name))
	})
	r.State = ChecksNone
	if len(r.Checks) > 0 {
		r.State = CheckPass
	}
	for _, c := range r.Checks {
		switch {
		case (c.State == CheckFail || c.State == CheckCancelled) && !c.Optional:
			r.State = CheckFail
			return r
		case c.State == CheckPending:
			r.State = CheckPending
		}
	}
	return r
}

// PrChecks returns the CI checks of a PR's head commit.
func (p Provider) PrChecks(ctx context.Context, nfo *Info, number int) (*ChecksReport, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		gh, err := githubClient(ctx, nfo)
		if err != nil {
			return nil, err
		}
		pr, _, err := gh.PullRequests.Get(ctx, nfo.Owner, nfo.Repo, number)
		if err != nil {
			return nil, err
		}
		return githubChecks(ctx, gh, nfo, number, pr.GetHead().GetSHA())
	case ProviderGitLab:
		gl, err := gitlabClient(ctx, nfo)
		if err != nil {
			return nil, err
		}
		mr, _, err := gl.MergeRequests.GetMergeRequest(gitlabProject(nfo), int64(number), nil)
		if err != nil {
			return nil, err
		}
		return gitlabChecks(gl, nfo, number, mr)
	case ProviderBitbucket:
		bb, err := newBitbucketAPIClient(nfo)
		if err != nil {
			return nil, err
		}
		pr, err := bitbucketGetPR(ctx, bb, nfo, number)
		if err != nil {
			return nil, err
		}
		return bitbucketChecks(ctx, bb, nfo, pr)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

// githubChecks combines the check runs (GitHub Actions and other apps) and
// the commit statuses of a commit.
func githubChecks(ctx context.Context, gh *githublib.Client, nfo *Info, number int, sha string) (*ChecksReport, error) {
	var (
		wg                 sync.WaitGroup
		runs, statuses     []Check
		runsErr, statusErr error
	)
	wg.Go(func() { runs, runsErr = githubCheckRuns(ctx, gh, nfo, sha) })
	wg.Go(func() { statuses, statusErr = githubStatuses(ctx, gh, nfo, sha) })
	wg.Wait()
	if err := cmp.Or(runsErr, statusErr); err != nil {
		return nil, err
	}
	return (&ChecksReport{PR: number, Commit: sha, Checks: append(runs, statuses...)}).finish(), nil
}

func githubCheckRuns(ctx context.Context, gh *githublib.Client, nfo *Info, sha string) ([]Check, error) {
	out := []Check{}
	opts := &githublib.ListCheckRunsOptions{Filter: new("latest"), ListOptions: githublib.ListOptions{PerPage: 100}}
	for {
		res, resp, err := gh.Checks.ListCheckRunsForRef(ctx, nfo.Owner, nfo.Repo, sha, opts)
		if err != nil {
			return nil, err
		}
		for _, r := range res.CheckRuns {
			out = append(out, Check{
				Name:          r.GetName(),
				State:         githubCheckRunState(r.GetStatus(), r.GetConclusion()),
				ProviderState: cmp.Or(r.GetConclusion(), r.GetStatus()),
				Description:   r.GetOutput().GetTitle(),
				URL:           cmp.Or(r.GetDetailsURL(), r.GetHTMLURL()),
				StartedAt:     r.GetStartedAt().Time,
				CompletedAt:   r.GetCompletedAt().Time,
			})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// githubCheckRunState maps a check run's status (queued, in_progress,
// completed, ...) and conclusion.
func githubCheckRunState(status, conclusion string) string {
	if status != "completed" {
		return CheckPending
	}
	switch conclusion {
	case "success":
		return CheckPass
	case "neutral", "skipped":
		return CheckSkipped
	case "cancelled":
		return CheckCancelled
	default: // failure, timed_out, action_required, startup_failure, stale
		return CheckFail
	}
}

// githubStatuses returns the latest commit status of each context.
func githubStatuses(ctx context.Context, gh *githublib.Client, nfo *Info, sha string) ([]Check, error) {
	out := []Check{}
	opts := &githublib.ListOptions{PerPage: 100}
	for {
		combined, resp, err := gh.Repositories.GetCombinedStatus(ctx, nfo.Owner, nfo.Repo, sha, opts)
		if err != nil {
			return nil, err
		}
		for _, s := range combined.Statuses {
			state := CheckFail // error, failure
			switch s.GetState() {
			case "success":
				state = CheckPass
			case "pending":
				state = CheckPending
			}
			out = append(out, Check{
				Name:          s.GetContext(),
				State:         state,
				ProviderState: s.GetState(),
				Description:   s.GetDescription(),
				URL:           s.GetTargetURL(),
				StartedAt:     s.GetCreatedAt().Time,
			})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// gitlabChecks lists the jobs (including trigger jobs of child pipelines) of
// the MR's head pipeline, which is what "Pipelines must succeed" checks.
func gitlabChecks(gl *gitlab.Client, nfo *Info, number int, mr *gitlab.MergeRequest) (*ChecksReport, error) {
	r := &ChecksReport{PR: number, Commit: mr.SHA, Checks: []Check{}}
	p := mr.HeadPipeline
	if p == nil {
		return r.finish(), nil
	}
	r.Commit, r.URL = cmp.Or(p.SHA, mr.SHA), p.WebURL
	// Pipelines of fork MRs may run in the fork.
	var project any = gitlabProject(nfo)
	if p.ProjectID != 0 {
		project = p.ProjectID
	}
	opts := &gitlab.ListJobsOptions{ListOptions: gitlab.ListOptions{PerPage: 100}}
	for {
		jobs, resp, err := gl.Jobs.ListPipelineJobs(project, p.ID, opts)
		if err != nil {
			return nil, err
		}
		for _, j := range jobs {
			r.Checks = append(r.Checks, gitlabJobCheck(j.Name, j.Stage, j.Status, j.AllowFailure, j.WebURL, j.StartedAt, j.FinishedAt))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	opts.Page = 0
	for {
		bridges, resp, err := gl.Jobs.ListPipelineBridges(project, p.ID, opts)
		if err != nil {
			return nil, err
		}
		for _, b := range bridges {
			r.Checks = append(r.Checks, gitlabJobCheck(b.Name, b.Stage, b.Status, b.AllowFailure, b.WebURL, b.StartedAt, b.FinishedAt))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return r.finish(), nil
}

func gitlabJobCheck(name, stage, status string, allowFailure bool, webURL string, started, finished *time.Time) Check {
	c := Check{Name: name, Group: stage, ProviderState: status, Optional: allowFailure, URL: webURL}
	switch status {
	case "success":
		c.State = CheckPass
	case "failed":
		c.State = CheckFail
	case "canceled", "canceling":
		c.State = CheckCancelled
	case "skipped", "manual":
		// Manual jobs wait for someone to start them.
		c.State = CheckSkipped
	default: // created, waiting_for_resource, preparing, pending, running, scheduled
		c.State = CheckPending
	}
	if started != nil {
		c.StartedAt = *started
	}
	if finished != nil {
		c.CompletedAt = *finished
	}
	return c
}

// bitbucketStatus is a Bitbucket commit (build) status.
type bitbucketStatus struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Description string `json:"description"`
	URL         string `json:"url"`
	CreatedOn   string `json:"created_on"`
	UpdatedOn   string `json:"updated_on"`
	Links       struct {
		Commit struct {
			Href string `json:"href"`
		} `json:"commit"`
	} `json:"links"`
}

// bitbucketChecks lists the build statuses of the PR's source commit.
// Statuses of earlier commits of the PR are skipped.
func bitbucketChecks(ctx context.Context, bb *bitbucketAPIClient, nfo *Info, pr *bitbucketPR) (*ChecksReport, error) {
	head := pr.Source.Commit.Hash // abbreviated
	r := &ChecksReport{PR: pr.ID, Commit: head, Checks: []Check{}}
	latest := map[string]bitbucketStatus{}
	err := bitbucketListPages(ctx, bb, bitbucketPREndpoint(nfo, pr.ID)+"/statuses", url.Values{}, func(s bitbucketStatus) bool {
		if head != "" && s.Links.Commit.Href != "" && !strings.HasPrefix(lastPathSegment(s.Links.Commit.Href), head) {
			return true
		}
		if prev, ok := latest[s.Key]; !ok || s.UpdatedOn >= prev.UpdatedOn {
			latest[s.Key] = s
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	for _, s := range latest {
		c := Check{
			Name:          cmp.Or(s.Name, s.Key),
			ProviderState: strings.ToLower(s.State),
			Description:   s.Description,
			URL:           s.URL,
		}
		switch s.State {
		case "SUCCESSFUL":
			c.State = CheckPass
		case "INPROGRESS":
			c.State = CheckPending
		case "STOPPED":
			c.State = CheckCancelled
		default: // FAILED
			c.State = CheckFail
		}
		c.StartedAt, _ = time.Parse(time.RFC3339, s.CreatedOn)
		if c.State != CheckPending {
			c.CompletedAt, _ = time.Parse(time.RFC3339, s.UpdatedOn)
		}
		r.Checks = append(r.Checks, c)
	}
	return r.finish(), nil
}

// lastPathSegment returns the last path segment of a URL.
func lastPathSegment(u string) string {
	if parsed, err := url.Parse(u); err == nil {
		u = parsed.Path
	}
	return u[strings.LastIndex(u, "/")+1:]
}
