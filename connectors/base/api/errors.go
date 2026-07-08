package api

import "errors"

// ErrUnsupported is returned for agents-api operations the connector runtime does not implement.
var ErrUnsupported = errors.New("operation not supported by the connector runtime")
