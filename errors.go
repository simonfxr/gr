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
	exitAuth     = 4 // credentials missing or rejected
	// pr checks results
	exitChecksFailed  = 5
	exitChecksPending = 6
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
	kindAuth        = "auth"
	kindUnsupported = "unsupported" // operation not offered by the provider
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

// exitStatus is a result reported by exit status alone, such as failing
// checks: the command's output already says it, so no error is printed.
type exitStatus struct {
	code int
	msg  string
}

func (e *exitStatus) Error() string { return e.msg }

func isSilent(err error) bool {
	_, ok := errors.AsType[*exitStatus](err)
	return ok
}

func errorKind(err error) string {
	if ce, ok := errors.AsType[*cliError](err); ok {
		return ce.kind
	}
	if _, ok := errors.AsType[*provider.InvalidArgumentError](err); ok {
		return kindUsage
	}
	if provider.IsUnsupported(err) {
		return kindUnsupported
	}
	if provider.IsAuthError(err) {
		return kindAuth
	}
	if provider.IsNotFound(err) {
		return kindNotFound
	}
	return kindError
}

func exitCode(err error) int {
	if es, ok := errors.AsType[*exitStatus](err); ok {
		return es.code
	}
	switch errorKind(err) {
	case kindUsage, kindUnsupported:
		return exitUsage
	case kindNotFound, kindAmbiguous:
		return exitNotFound
	case kindAuth:
		return exitAuth
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
