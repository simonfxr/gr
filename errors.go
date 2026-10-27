package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/simonfxr/gr/pkg/provider"
)

// Exit codes. Usage errors match go-arg's exit status for invalid arguments.
const (
	exitError    = 1 // any other failure (API, network, git, ...)
	exitUsage    = 2 // invalid or conflicting arguments
	exitNotFound = 3 // PR (or comment/task) not found, or PR selection ambiguous
	// exitInterrupted follows the shell convention 128+SIGINT.
	exitInterrupted = 130
)

// Error kinds reported in JSON error output.
const (
	kindError       = "error"
	kindUsage       = "usage"
	kindNotFound    = "not_found"
	kindAmbiguous   = "ambiguous"
	kindInterrupted = "interrupted"
)

// cliError is an error with a machine-readable kind.
type cliError struct {
	kind string
	msg  string
	// candidates lists the matching PRs for kindAmbiguous.
	candidates []provider.PullRequest
}

func (e *cliError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return &cliError{kind: kindUsage, msg: fmt.Sprintf(format, args...)}
}

func notFoundErrorf(format string, args ...any) error {
	return &cliError{kind: kindNotFound, msg: fmt.Sprintf(format, args...)}
}

func errorKind(err error) string {
	if ce, ok := errors.AsType[*cliError](err); ok {
		return ce.kind
	}
	if _, ok := errors.AsType[*provider.InvalidArgumentError](err); ok {
		return kindUsage
	}
	if provider.IsNotFound(err) {
		return kindNotFound
	}
	return kindError
}

func exitCode(err error) int {
	switch errorKind(err) {
	case kindUsage:
		return exitUsage
	case kindNotFound, kindAmbiguous:
		return exitNotFound
	case kindInterrupted:
		return exitInterrupted
	default:
		return exitError
	}
}

// reportError writes err to w, as a JSON object when jsonMode is set.
func reportError(w io.Writer, err error, jsonMode bool) {
	var candidates []provider.PullRequest
	if ce, ok := errors.AsType[*cliError](err); ok {
		candidates = ce.candidates
	}
	if jsonMode {
		b, _ := json.MarshalIndent(struct {
			Error      string                 `json:"error"`
			Kind       string                 `json:"kind"`
			ExitCode   int                    `json:"exit_code"`
			Candidates []provider.PullRequest `json:"candidates,omitempty"`
		}{err.Error(), errorKind(err), exitCode(err), candidates}, "", "  ")
		fmt.Fprintln(w, string(b))
		return
	}
	fmt.Fprintf(w, "Error: %v\n", err)
	for _, pr := range candidates {
		fmt.Fprintf(w, "  #%d %s (%s)\n", pr.Number, pr.Title, pr.URL)
	}
}

// wantsJSON reports whether the selected (sub)command has --json set.
func wantsJSON(cmd any) bool {
	v := reflect.ValueOf(cmd)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	f := v.FieldByName("JSON")
	return f.IsValid() && f.Kind() == reflect.Bool && f.Bool()
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
