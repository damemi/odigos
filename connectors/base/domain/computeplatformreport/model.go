// Package computeplatformreport converts discovered compute platforms (managed infra Odigos is installed on, not
// instrumented) to the agents-api ComputePlatformReport model — the compute-platform-domain counterpart to the
// workloadreport package.
package computeplatformreport

import (
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// ToModel converts a discovered compute platform to the agents-api ComputePlatformReport. Assignment state is applied by
// the coordinator before this mapping via DiscoveredComputePlatform.Status.
func ToModel(s api.DiscoveredComputePlatform) *model.ComputePlatformReport {
	updated := s.LastSeenAt.UTC().Format(time.RFC3339)
	status := s.Status
	if status == "" {
		status = "Discovered"
	}
	return &model.ComputePlatformReport{
		Name:          s.Name,
		Kind:          s.Kind,
		Region:        strPtrOrNil(s.Region),
		ExternalID:    strPtrOrNil(s.ExternalID),
		Status:        strPtrOrNil(status),
		OdigosVersion: strPtrOrNil(s.OdigosVersion),
		Metadata:      s.Metadata,
		UpdatedAt:     updated,
	}
}

func strPtrOrNil(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
