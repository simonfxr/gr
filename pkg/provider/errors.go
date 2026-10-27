package provider

import "fmt"

// InvalidArgumentError reports a caller error detected before contacting the
// provider (e.g. an empty body or unknown filter value).
type InvalidArgumentError struct{ msg string }

func (e *InvalidArgumentError) Error() string { return e.msg }

func invalidArgf(format string, args ...any) error {
	return &InvalidArgumentError{msg: fmt.Sprintf(format, args...)}
}
