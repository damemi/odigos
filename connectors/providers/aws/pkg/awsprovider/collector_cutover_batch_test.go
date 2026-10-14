package awsprovider

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcutover"
)

func TestCollectorBatchQuotaIsSharedFairlyAcrossKinds(t *testing.T) {
	for _, size := range []int{1, 2, 3, 10, 11} {
		for _, pass := range []uint64{1, 2} {
			fargate := collectorBatchQuota(size, "Fargate", true, pass)
			lambda := collectorBatchQuota(size, "Lambda", true, pass)
			if fargate+lambda != size {
				t.Errorf("size %d pass %d: Fargate %d + Lambda %d exceeds regional batch", size, pass, fargate, lambda)
			}
		}
	}
	if collectorBatchQuota(1, "Fargate", true, 1) == collectorBatchQuota(1, "Fargate", true, 2) {
		t.Fatal("batch size one did not alternate between Fargate and Lambda")
	}
	if got := collectorBatchQuota(3, "Fargate", false, 1); got != 3 {
		t.Fatalf("single active kind quota = %d", got)
	}
	if got := collectorBatchQuota(0, "Fargate", false, 1); got != collectorcutover.DefaultBatchSize {
		t.Fatalf("default quota = %d", got)
	}
}
