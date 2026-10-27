package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/simonfxr/gr/pkg/provider"
	"github.com/simonfxr/gr/tables"
)

// checksAppearTimeout is how long `pr checks --watch` waits for the first
// checks of a PR, e.g. right after a push.
var checksAppearTimeout = time.Minute

func prChecks(ctx context.Context, info *provider.Info, cmd *PRChecksCmd) error {
	if cmd.Watch && cmd.Interval <= 0 {
		return usageErrorf("--interval must be positive")
	}
	if cmd.FailFast && !cmd.Watch {
		return usageErrorf("--fail-fast requires --watch")
	}
	number, err := resolvePR(ctx, info, cmd.PR, cmd.PRSelector)
	if err != nil {
		return err
	}
	report, err := watchChecks(ctx, info, number, cmd)
	if err != nil {
		return err
	}
	if cmd.JSON {
		if err := printJSON(report); err != nil {
			return err
		}
	} else {
		printChecks(report)
	}
	switch report.State {
	case provider.CheckFail:
		return &exitStatus{code: exitChecksFailed, msg: fmt.Sprintf("checks of PR #%d failed", number)}
	case provider.CheckPending:
		return &exitStatus{code: exitChecksPending, msg: fmt.Sprintf("checks of PR #%d are pending", number)}
	default:
		return nil
	}
}

// watchChecks fetches the checks of a PR; with --watch it polls until no
// check is pending (or one failed, with --fail-fast), reporting progress on
// stderr.
func watchChecks(ctx context.Context, info *provider.Info, number int, cmd *PRChecksCmd) (*provider.ChecksReport, error) {
	appearBy := time.Now().Add(checksAppearTimeout)
	lastProgress := ""
	for {
		report, err := info.Provider.PrChecks(ctx, info, number)
		if err != nil {
			return nil, err
		}
		switch {
		case !cmd.Watch:
			return report, nil
		case cmd.FailFast && report.State == provider.CheckFail:
			return report, nil
		case len(report.Checks) == 0 && time.Now().After(appearBy):
			return report, nil
		case len(report.Checks) > 0 && report.Pending() == 0:
			return report, nil
		}
		progress := "waiting for checks to appear"
		if len(report.Checks) > 0 {
			progress = "waiting: " + checksSummary(report)
		}
		if progress != lastProgress {
			fmt.Fprintf(os.Stderr, "PR #%d: %s\n", number, progress)
			lastProgress = progress
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cmd.Interval):
		}
	}
}

func printChecks(r *provider.ChecksReport) {
	if len(r.Checks) == 0 {
		fmt.Printf("No checks reported for PR #%d.\n", r.PR)
		return
	}
	if tables.StdoutIsTerminal() {
		fmt.Printf("PR #%d: %s\n\n", r.PR, checksSummary(r))
	}
	headers := []string{"State", "Check", "Description", "URL"}
	tables.Render(headers, func(yield func([]string) bool) {
		for _, c := range r.Checks {
			if !yield([]string{checkState(c), checkName(c), c.Description, c.URL}) {
				return
			}
		}
	})
}

// checksSummary counts checks by state, e.g. "1 failed, 3 passed, 1 pending".
func checksSummary(r *provider.ChecksReport) string {
	counts := map[string]int{}
	for _, c := range r.Checks {
		counts[c.State]++
	}
	var parts []string
	for _, s := range []struct{ state, label string }{
		{provider.CheckFail, "failed"},
		{provider.CheckCancelled, "cancelled"},
		{provider.CheckPending, "pending"},
		{provider.CheckPass, "passed"},
		{provider.CheckSkipped, "skipped"},
	} {
		if n := counts[s.state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s.label))
		}
	}
	if len(parts) == 0 {
		return "no checks"
	}
	return strings.Join(parts, ", ")
}

// checkState renders a check's state, marking failures that are allowed.
func checkState(c provider.Check) string {
	if c.Optional && (c.State == provider.CheckFail || c.State == provider.CheckCancelled) {
		return c.State + " (allowed)"
	}
	return c.State
}

// checkName renders a check's name, prefixed by its GitLab stage.
func checkName(c provider.Check) string {
	if c.Group != "" {
		return c.Group + " / " + c.Name
	}
	return c.Name
}
