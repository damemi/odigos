package api

import "errors"

// ErrUnsupported is returned for agents-api operations the connector runtime does not implement.
var ErrUnsupported = errors.New("operation not supported by the connector runtime")

// ErrCollectorTeardownRetry marks cloud release lag (for example an NLB ENI
// still holding its security group). The coordinator keeps teardown pending
// and retries it on the next reconcile pass.
var ErrCollectorTeardownRetry = errors.New("Collector teardown needs a retry")
