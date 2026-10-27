package v1alpha1

import (
	"errors"
	"strings"
)

// joinErrors collapses a slice of errors into a single error whose message lists all failures,
// separated by "; ". Returns nil when the slice is empty or contains only nil entries.
//
// This is used by webhook validators to surface every validation failure in a single API response
// rather than requiring the operator to fix-and-retry one error at a time.
func joinErrors(errs []error) error {
	var msgs []string
	for _, e := range errs {
		if e != nil {
			msgs = append(msgs, e.Error())
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return errors.New(strings.Join(msgs, "; "))
}
