package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// PRStatus is a PR with everything that decides whether it can be merged:
// reviews, checks, open discussions and the provider's merge verdict.
type PRStatus struct {
	PRDetails
	// Reviews, Checks and Comments are nil if they could not be fetched;
	// Warnings then says why.
	Reviews  *Reviews       `json:"reviews"`
	Checks   *ChecksReport  `json:"checks"`
	Comments *CommentCounts `json:"comments"`
	// Tasks is set on Bitbucket only.
	Tasks    *TaskCounts `json:"tasks,omitempty"`
	Merge    MergeStatus `json:"merge"`
	Warnings []string    `json:"warnings,omitempty"`
}

// Reviewer states.
const (
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	ReviewCommented        = "commented"
	ReviewPending          = "pending" // asked to review, no verdict (yet)
)

// Review decisions; "" means no verdict.
const (
	DecisionApproved         = "approved"
	DecisionChangesRequested = "changes_requested"
	DecisionReviewRequired   = "review_required" // the provider requires (more) approvals
)

// Reviews summarizes the reviews of a PR.
type Reviews struct {
	// Decision is the provider's review verdict where it reports one
	// (GitHub reviewDecision, GitLab approval rules); otherwise
	// DecisionChangesRequested if anyone requested changes, else
	// DecisionApproved if anyone approved, else "".
	Decision  string `json:"decision"`
	Approvals int    `json:"approvals"`
	// ApprovalsRequired is the number of approvals GitLab's approval rules
	// require; 0 if none or unknown.
	ApprovalsRequired int        `json:"approvals_required,omitempty"`
	Reviewers         []Reviewer `json:"reviewers"`
}

// Reviewer is a user (or GitHub team) who reviewed or was asked to.
type Reviewer struct {
	Login string `json:"login"`
	// State is ReviewApproved, ReviewChangesRequested, ReviewCommented or
	// ReviewPending.
	State string `json:"state"`
	// Requested is set for requested (assigned) reviewers.
	Requested bool `json:"requested"`
}

// CommentCounts counts the comments of a PR, as listed by `pr comments`.
type CommentCounts struct {
	Total int `json:"total"`
	// Unresolved counts unresolved threads that can be resolved.
	Unresolved int `json:"unresolved"`
}

// TaskCounts counts the tasks of a Bitbucket PR.
type TaskCounts struct {
	Open     int `json:"open"`
	Resolved int `json:"resolved"`
}

// Merge blockers, in the order they are reported.
const (
	BlockerNotOpen            = "not_open"
	BlockerDraft              = "draft"
	BlockerConflicts          = "conflicts"
	BlockerBehind             = "behind" // the base branch must be merged in first
	BlockerChecksFailed       = "checks_failed"
	BlockerChecksPending      = "checks_pending"
	BlockerChangesRequested   = "changes_requested"
	BlockerApprovalRequired   = "approval_required"
	BlockerUnresolvedComments = "unresolved_comments"
	BlockerOpenTasks          = "open_tasks"
	BlockerBlocked            = "blocked" // by other provider rules, see ProviderStatus
)

var blockerOrder = []string{
	BlockerNotOpen, BlockerDraft, BlockerConflicts, BlockerBehind, BlockerChecksFailed, BlockerChecksPending,
	BlockerChangesRequested, BlockerApprovalRequired, BlockerUnresolvedComments, BlockerOpenTasks, BlockerBlocked,
}

// MergeStatus tells whether a PR can be merged.
type MergeStatus struct {
	// Mergeable is the provider's verdict: GitHub mergeable_state, GitLab
	// detailed_merge_status. It is nil while the provider is still checking,
	// and always on Bitbucket, which does not report one.
	Mergeable *bool `json:"mergeable"`
	// ProviderStatus is the provider's raw merge status.
	ProviderStatus string `json:"provider_status,omitempty"`
	// Blockers lists what stands between the PR and merging, whether or
	// not the provider enforces it (e.g. unresolved comments).
	Blockers []string `json:"blockers"`

	// providerBlocked is set when the provider reports the PR as blocked
	// without saying why (GitHub "blocked").
	providerBlocked bool
}

// PrStatus fetches a PR together with its reviews, checks and comment
// counts. Parts that cannot be fetched (e.g. for lack of token permissions)
// are left nil and reported in Warnings.
func (p Provider) PrStatus(ctx context.Context, nfo *Info, number int) (*PRStatus, error) {
	if nfo == nil {
		return nil, errors.New("missing repo info")
	}
	switch p {
	case ProviderGitHub:
		return prStatusGitHub(ctx, nfo, number)
	case ProviderGitLab:
		return prStatusGitLab(ctx, nfo, number)
	case ProviderBitbucket:
		return prStatusBitbucket(ctx, nfo, number)
	default:
		return nil, fmt.Errorf("unknown provider: %v", p)
	}
}

// statusFetch runs the parts of a PRStatus concurrently. Failed parts
// become warnings.
type statusFetch struct {
	ctx      context.Context
	wg       sync.WaitGroup
	mu       sync.Mutex
	warnings []string
}

func (f *statusFetch) run(what string, fn func() error) {
	f.wg.Go(func() {
		if err := fn(); err != nil {
			f.mu.Lock()
			f.warnings = append(f.warnings, fmt.Sprintf("%s: %v", what, err))
			f.mu.Unlock()
		}
	})
}

// finish waits for all parts and completes st.
func (f *statusFetch) finish(st *PRStatus) (*PRStatus, error) {
	f.wg.Wait()
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	slices.Sort(f.warnings)
	st.Warnings = f.warnings
	st.addBlockers()
	return st, nil
}

func (f *statusFetch) comments(st *PRStatus, list func() ([]PRComment, error)) {
	f.run("comments", func() error {
		comments, err := list()
		if err != nil {
			return err
		}
		st.Comments = &CommentCounts{Total: len(comments)}
		for _, c := range comments {
			if c.ParentID == 0 && c.Resolvable && !c.Resolved {
				st.Comments.Unresolved++
			}
		}
		return nil
	})
}

// addBlockers completes Merge.Blockers from the PR's state, reviews,
// checks and open discussions.
func (st *PRStatus) addBlockers() {
	m := &st.Merge
	if st.State != StateOpen {
		m.Blockers = []string{BlockerNotOpen}
		return
	}
	set := map[string]bool{}
	for _, b := range m.Blockers {
		set[b] = true
	}
	set[BlockerDraft] = set[BlockerDraft] || st.Draft
	if c := st.Checks; c != nil {
		set[BlockerChecksFailed] = set[BlockerChecksFailed] || c.State == CheckFail
		set[BlockerChecksPending] = set[BlockerChecksPending] || c.State == CheckPending
	}
	if r := st.Reviews; r != nil {
		set[BlockerChangesRequested] = set[BlockerChangesRequested] || r.Decision == DecisionChangesRequested
		set[BlockerApprovalRequired] = set[BlockerApprovalRequired] || r.Decision == DecisionReviewRequired
	}
	set[BlockerUnresolvedComments] = set[BlockerUnresolvedComments] || (st.Comments != nil && st.Comments.Unresolved > 0)
	set[BlockerOpenTasks] = set[BlockerOpenTasks] || (st.Tasks != nil && st.Tasks.Open > 0)
	m.Blockers = []string{}
	for _, b := range blockerOrder {
		if set[b] {
			m.Blockers = append(m.Blockers, b)
		}
	}
	if m.providerBlocked && len(m.Blockers) == 0 {
		m.Blockers = append(m.Blockers, BlockerBlocked)
	}
}

// reviewsFrom completes Reviews from its reviewers: Approvals and, if the
// provider gave none, Decision.
func reviewsFrom(decision string, reviewers []Reviewer) *Reviews {
	r := &Reviews{Decision: decision, Reviewers: reviewers}
	if r.Reviewers == nil {
		r.Reviewers = []Reviewer{}
	}
	changes := false
	for _, rv := range reviewers {
		switch rv.State {
		case ReviewApproved:
			r.Approvals++
		case ReviewChangesRequested:
			changes = true
		}
	}
	if r.Decision == "" {
		switch {
		case changes:
			r.Decision = DecisionChangesRequested
		case r.Approvals > 0:
			r.Decision = DecisionApproved
		}
	}
	return r
}

// reviewerList collects reviewers by login, keeping first-seen order.
type reviewerList struct {
	list  []Reviewer
	index map[string]int
}

// get returns the reviewer with login, adding a pending one if needed.
func (l *reviewerList) get(login string) *Reviewer {
	if l.index == nil {
		l.index = map[string]int{}
	}
	i, ok := l.index[login]
	if !ok {
		i = len(l.list)
		l.index[login] = i
		l.list = append(l.list, Reviewer{Login: login, State: ReviewPending})
	}
	return &l.list[i]
}

// GitHub

func prStatusGitHub(ctx context.Context, nfo *Info, number int) (*PRStatus, error) {
	gh, err := githubClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	pr, _, err := gh.PullRequests.Get(ctx, nfo.Owner, nfo.Repo, number)
	if err != nil {
		return nil, err
	}
	st := &PRStatus{PRDetails: *githubPRDetails(pr), Merge: githubMergeStatus(pr)}
	f := &statusFetch{ctx: ctx}
	f.run("reviews", func() (err error) {
		st.Reviews, err = githubReviews(ctx, gh, nfo, number)
		return err
	})
	f.run("checks", func() (err error) {
		st.Checks, err = githubChecks(ctx, gh, nfo, number, pr.GetHead().GetSHA())
		return err
	})
	f.comments(st, func() ([]PRComment, error) { return prCommentsGitHub(ctx, nfo, number) })
	return f.finish(st)
}

// githubMergeStatus maps mergeable_state (clean, unstable, has_hooks,
// dirty, behind, blocked, draft, unknown). GitHub computes it in the
// background; it is "unknown" until then.
func githubMergeStatus(pr *githublib.PullRequest) MergeStatus {
	m := MergeStatus{ProviderStatus: pr.GetMergeableState()}
	switch m.ProviderStatus {
	case "clean", "unstable", "has_hooks":
		m.Mergeable = new(true)
	case "dirty":
		m.Mergeable, m.Blockers = new(false), []string{BlockerConflicts}
	case "behind":
		m.Mergeable, m.Blockers = new(false), []string{BlockerBehind}
	case "draft":
		m.Mergeable, m.Blockers = new(false), []string{BlockerDraft}
	case "blocked":
		m.Mergeable, m.providerBlocked = new(false), true
	}
	if pr.Mergeable != nil && !*pr.Mergeable {
		m.Blockers = append(m.Blockers, BlockerConflicts)
	}
	return m
}

// githubReviews combines each user's latest review with the open review
// requests. Users with a review that counts (approve, request changes) keep
// it even if they commented later.
func githubReviews(ctx context.Context, gh *githublib.Client, nfo *Info, number int) (*Reviews, error) {
	const query = `query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewDecision
      latestReviews(first: 100) { nodes { state author { login } } }
      latestOpinionatedReviews(first: 100) { nodes { state author { login } } }
      reviewRequests(first: 100) {
        nodes {
          requestedReviewer {
            ... on User { login }
            ... on Bot { login }
            ... on Mannequin { login }
            ... on Team { slug organization { login } }
          }
        }
      }
    }
  }
}`
	type review struct {
		State  string
		Author *struct{ Login string }
	}
	var res struct {
		Repository *struct {
			PullRequest *struct {
				ReviewDecision           string
				LatestReviews            struct{ Nodes []review }
				LatestOpinionatedReviews struct{ Nodes []review }
				ReviewRequests           struct {
					Nodes []struct {
						RequestedReviewer *struct {
							Login        string
							Slug         string
							Organization *struct{ Login string }
						}
					}
				}
			}
		}
	}
	vars := map[string]any{"owner": nfo.Owner, "repo": nfo.Repo, "number": number}
	if err := githubGraphQL(ctx, gh, query, vars, &res); err != nil {
		return nil, err
	}
	if res.Repository == nil || res.Repository.PullRequest == nil {
		return nil, notFoundf("pull request #%d not found", number)
	}
	pr := res.Repository.PullRequest
	var l reviewerList
	for _, rv := range slices.Concat(pr.LatestReviews.Nodes, pr.LatestOpinionatedReviews.Nodes) {
		state := map[string]string{
			"APPROVED": ReviewApproved, "CHANGES_REQUESTED": ReviewChangesRequested,
			"COMMENTED": ReviewCommented, "DISMISSED": ReviewCommented,
		}[rv.State]
		if rv.Author == nil || rv.Author.Login == "" || state == "" {
			continue
		}
		// Opinionated reviews come last and win over later comments.
		l.get(rv.Author.Login).State = state
	}
	for _, n := range pr.ReviewRequests.Nodes {
		r := n.RequestedReviewer
		if r == nil {
			continue
		}
		login := r.Login
		if r.Slug != "" {
			login = r.Slug
			if r.Organization != nil {
				login = r.Organization.Login + "/" + r.Slug
			}
		}
		if login != "" {
			l.get(login).Requested = true
		}
	}
	return reviewsFrom(strings.ToLower(pr.ReviewDecision), l.list), nil
}

// GitLab

func prStatusGitLab(ctx context.Context, nfo *Info, number int) (*PRStatus, error) {
	gl, err := gitlabClient(ctx, nfo)
	if err != nil {
		return nil, err
	}
	mr, _, err := gl.MergeRequests.GetMergeRequest(gitlabProject(nfo), int64(number), nil)
	if err != nil {
		return nil, err
	}
	st := &PRStatus{PRDetails: *gitlabPRDetails(&mr.BasicMergeRequest), Merge: gitlabMergeStatus(&mr.BasicMergeRequest)}
	f := &statusFetch{ctx: ctx}
	f.run("reviews", func() (err error) {
		st.Reviews, err = gitlabReviews(gl, nfo, number, mr.DetailedMergeStatus)
		return err
	})
	f.run("checks", func() (err error) {
		st.Checks, err = gitlabChecks(gl, nfo, number, mr)
		return err
	})
	f.comments(st, func() ([]PRComment, error) { return prCommentsGitLab(ctx, nfo, number) })
	return f.finish(st)
}

// gitlabMergeBlockers maps GitLab's detailed_merge_status values that block
// merging; other values than these, "mergeable" and the transient ones
// block for other reasons (BlockerBlocked).
var gitlabMergeBlockers = map[string]string{
	"not_open":                 BlockerNotOpen,
	"draft_status":             BlockerDraft,
	"conflict":                 BlockerConflicts,
	"need_rebase":              BlockerBehind,
	"ci_must_pass":             BlockerChecksFailed,
	"ci_still_running":         BlockerChecksPending,
	"requested_changes":        BlockerChangesRequested,
	"not_approved":             BlockerApprovalRequired,
	"discussions_not_resolved": BlockerUnresolvedComments,
}

// gitlabMergeStatus maps detailed_merge_status, which names the first
// failed merge check.
func gitlabMergeStatus(mr *gitlab.BasicMergeRequest) MergeStatus {
	m := MergeStatus{ProviderStatus: mr.DetailedMergeStatus}
	switch s := mr.DetailedMergeStatus; s {
	case "mergeable":
		m.Mergeable = new(true)
	case "", "unchecked", "checking", "preparing", "approvals_syncing":
		// Still being checked.
	default:
		m.Mergeable = new(false)
		m.Blockers = []string{BlockerBlocked}
		if b, ok := gitlabMergeBlockers[s]; ok {
			m.Blockers = []string{b}
		}
	}
	if mr.HasConflicts && !slices.Contains(m.Blockers, BlockerConflicts) {
		m.Blockers = append(m.Blockers, BlockerConflicts)
	}
	return m
}

// gitlabReviewerStates maps GitLab reviewer states.
var gitlabReviewerStates = map[string]string{
	"approved":          ReviewApproved,
	"requested_changes": ReviewChangesRequested,
	"reviewed":          ReviewCommented,
}

// gitlabReviews combines the MR's reviewers with its approvals, which may
// come from users who are not reviewers.
func gitlabReviews(gl *gitlab.Client, nfo *Info, number int, mergeStatus string) (*Reviews, error) {
	reviewers, _, err := gl.MergeRequests.GetMergeRequestReviewers(gitlabProject(nfo), int64(number))
	if err != nil {
		return nil, err
	}
	approvals, _, err := gl.MergeRequests.GetMergeRequestApprovals(gitlabProject(nfo), int64(number))
	if err != nil {
		return nil, err
	}
	var l reviewerList
	for _, rv := range reviewers {
		if rv.User == nil {
			continue
		}
		r := l.get(rv.User.Username)
		r.Requested = true
		if s, ok := gitlabReviewerStates[rv.State]; ok {
			r.State = s
		}
	}
	for _, a := range approvals.ApprovedBy {
		if a.User != nil {
			l.get(a.User.Username).State = ReviewApproved
		}
	}
	decision := ""
	switch {
	case mergeStatus == "requested_changes" || slices.ContainsFunc(l.list, func(r Reviewer) bool { return r.State == ReviewChangesRequested }):
		decision = DecisionChangesRequested
	case mergeStatus == "not_approved" || approvals.ApprovalsLeft > 0:
		decision = DecisionReviewRequired
	}
	r := reviewsFrom(decision, l.list)
	// approvals_required and approvals_left exist on GitLab EE only.
	r.ApprovalsRequired = int(approvals.ApprovalsRequired)
	return r, nil
}

// Bitbucket

func prStatusBitbucket(ctx context.Context, nfo *Info, number int) (*PRStatus, error) {
	bb, err := newBitbucketAPIClient(nfo)
	if err != nil {
		return nil, err
	}
	pr, err := bitbucketGetPR(ctx, bb, nfo, number)
	if err != nil {
		return nil, err
	}
	// Bitbucket reports no merge verdict; its merge checks are not exposed.
	st := &PRStatus{PRDetails: *pr.toPRDetails(), Reviews: bitbucketReviews(pr)}
	f := &statusFetch{ctx: ctx}
	f.run("checks", func() (err error) {
		st.Checks, err = bitbucketChecks(ctx, bb, nfo, pr)
		return err
	})
	f.comments(st, func() ([]PRComment, error) { return prCommentsBitbucket(ctx, nfo, number) })
	f.run("tasks", func() error {
		tasks, err := prTasksBitbucket(ctx, nfo, number, TaskListOptions{State: "all"})
		if err != nil {
			return err
		}
		st.Tasks = &TaskCounts{}
		for _, t := range tasks {
			if t.Resolved {
				st.Tasks.Resolved++
			} else {
				st.Tasks.Open++
			}
		}
		return nil
	})
	return f.finish(st)
}

// bitbucketReviews derives the reviews from the PR's participants:
// reviewers, and others who approved, requested changes or commented.
func bitbucketReviews(pr *bitbucketPR) *Reviews {
	var l reviewerList
	for _, p := range pr.Participants {
		login := p.User.name()
		if login == "" {
			continue
		}
		r := l.get(login)
		r.Requested = p.Role == "REVIEWER"
		switch {
		case p.Approved || p.State == "approved":
			r.State = ReviewApproved
		case p.State == "changes_requested":
			r.State = ReviewChangesRequested
		case !r.Requested:
			r.State = ReviewCommented
		}
	}
	return reviewsFrom("", l.list)
}
