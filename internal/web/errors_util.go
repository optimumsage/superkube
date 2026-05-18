package web

import "errors"

// asErrAs is errors.As wrapped to keep handler files free of the errors
// import when they only need the As shorthand.
func asErrAs(err error, target any) bool {
	return errors.As(err, target)
}
