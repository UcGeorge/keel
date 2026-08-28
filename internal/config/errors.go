package config

import (
	"fmt"
	"strings"
)

// ValidationError is one problem found in a configuration document,
// anchored to a dotted path such as "deployments.aws.variables.REGION".
type ValidationError struct {
	Path    string
	Message string
}

func (e ValidationError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// ValidationErrors collects every problem found in a document so authors can
// fix them all at once.
type ValidationErrors struct {
	Errors []ValidationError
}

// Addf records a new validation error.
func (v *ValidationErrors) Addf(path, format string, args ...any) {
	v.Errors = append(v.Errors, ValidationError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// HasErrors reports whether any error was recorded.
func (v *ValidationErrors) HasErrors() bool { return len(v.Errors) > 0 }

func (v *ValidationErrors) Error() string {
	if len(v.Errors) == 0 {
		return "configuration is valid"
	}
	lines := make([]string, len(v.Errors))
	for i, e := range v.Errors {
		lines[i] = "  - " + e.Error()
	}
	return fmt.Sprintf("configuration has %d problem(s):\n%s", len(v.Errors), strings.Join(lines, "\n"))
}
