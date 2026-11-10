// Package sinks assembles the backend catalog. Adding a backend is a new
// package under sinks/ plus one line in NewRegistry; everything else
// (dispatcher, API, debug UI) is generic over the registry.
package sinks

import (
	"github.com/odigos-io/odigos/insights/internal/notify"
	"github.com/odigos-io/odigos/insights/internal/notify/sinks/alertmanager"
	"github.com/odigos-io/odigos/insights/internal/notify/sinks/slack"
	"github.com/odigos-io/odigos/insights/internal/notify/sinks/splunk"
	"github.com/odigos-io/odigos/insights/internal/notify/sinks/webhook"
)

// NewRegistry returns the registry with every shipped backend registered.
func NewRegistry(deps notify.Deps) *notify.Registry {
	r := notify.NewRegistry(deps)
	r.Register(alertmanager.Descriptor, alertmanager.New)
	r.Register(webhook.Descriptor, webhook.New)
	r.Register(slack.Descriptor, slack.New)
	r.Register(splunk.Descriptor, splunk.New)
	return r
}
