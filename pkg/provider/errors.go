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
	return httpStatus(err) == http.StatusNotFound
}

// IsAuthError reports whether err is caused by missing credentials or by
// credentials the provider rejected (HTTP 401).
func IsAuthError(err error) bool {
	if _, ok := errors.AsType[*AuthError](err); ok {
		return true
	}
	return httpStatus(err) == http.StatusUnauthorized
}

// httpStatus returns the HTTP status code of a provider API error, or 0.
func httpStatus(err error) int {
	if err == nil {
		return 0
	}
	if bb, ok := errors.AsType[*bitbucketAPIError](err); ok {
		return bb.status
	}
	if gh, ok := errors.AsType[*githublib.ErrorResponse](err); ok && gh.Response != nil {
		return gh.Response.StatusCode
	}
	if errors.Is(err, gitlab.ErrNotFound) {
		return http.StatusNotFound
	}
	if gl, ok := errors.AsType[*gitlab.ErrorResponse](err); ok && gl.Response != nil {
		return gl.Response.StatusCode
	}
	return 0
}

// AuthError reports missing credentials.
type AuthError struct{ msg string }

func (e *AuthError) Error() string { return e.msg }

// missingToken is the error for a provider without configured token.
func missingToken(name, env, section string) error {
	return &AuthError{msg: fmt.Sprintf("%s token not configured (set %s, or token or credential_helper under [%s] in ~/.config/gr/config.toml)", name, env, section)}
}

// UnsupportedError reports an operation that a provider does not offer,
// such as tasks outside Bitbucket.
type UnsupportedError struct {
	Op       string // e.g. "PR tasks"
	Provider Provider
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s: not supported on %s", e.Op, e.Provider)
}

// IsUnsupported reports whether err is an UnsupportedError.
func IsUnsupported(err error) bool {
	_, ok := errors.AsType[*UnsupportedError](err)
	return ok
}

func unsupported(op string, p Provider) error { return &UnsupportedError{Op: op, Provider: p} }

// InvalidArgumentError reports a caller error detected before contacting the
// provider (e.g. an empty body or unknown filter value).
type InvalidArgumentError struct{ msg string }

func (e *InvalidArgumentError) Error() string { return e.msg }

func invalidArgf(format string, args ...any) error {
	return &InvalidArgumentError{msg: fmt.Sprintf(format, args...)}
}
