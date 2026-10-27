package provider

import (
	"errors"
	"fmt"
	"net/http"

	githublib "github.com/google/go-github/v74/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// IsNotFound reports whether err is a provider API "404 Not Found" response,
// e.g. for a non-existent PR, comment or task.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if bbAPI, ok := errors.AsType[*bitbucketAPIError](err); ok {
		return bbAPI.status == http.StatusNotFound
	}
	if gh, ok := errors.AsType[*githublib.ErrorResponse](err); ok {
		return gh.Response != nil && gh.Response.StatusCode == http.StatusNotFound
	}
	return errors.Is(err, gitlab.ErrNotFound)
}

// InvalidArgumentError reports a caller error detected before contacting the
// provider (e.g. an empty body or unknown filter value).
type InvalidArgumentError struct{ msg string }

func (e *InvalidArgumentError) Error() string { return e.msg }

func invalidArgf(format string, args ...any) error {
	return &InvalidArgumentError{msg: fmt.Sprintf(format, args...)}
}
