package autopilot

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/insights/internal/autopilot/measure"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
)

// checkoutIdentity is the source identity of shop/Deployment/checkout's app
// container.
var checkoutIdentity = map[string]string{
	semconv.K8SNamespaceName:           "shop",
	consts.OdigosWorkloadKindAttribute: "Deployment",
	consts.OdigosWorkloadNameAttribute: "checkout",
	semconv.K8SContainerName:           "app",
}

// Two replicas flush into the same hash; the reader sees their sum.
func TestMeasuresMergeAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	r := &tracesRedis{rdb: rdb}

	now := time.Unix(1005, 0)
	replica := func(site string, durUs int64, msg string) {
		tx := ServiceInvocation{SourceIdentity: checkoutIdentity, TransactionID: "7"}
		for range 10 {
			tx.Measures = append(tx.Measures,
				measure.Sample{Bucket: 1000, Site: site, DurUs: durUs, SelfUs: durUs / 2, Err: msg != "", Msg: msg + "\x00\n"},
				measure.Sample{Bucket: 1000, DurUs: 2 * durUs, SelfUs: durUs, Spans: 4})
		}
		set := measureSet{}
		set.add(tx)
		if err := r.mergeMeasures(ctx, set); err != nil {
			t.Fatal(err)
		}
	}
	replica("app.Svc.buy", 4000, "boom")
	replica("app.Svc.buy", 16000, "bang")
	replica("app.Svc.buy", 8000, "boom")

	got, err := r.probeTimings(ctx, "shop", "Deployment", "checkout", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	site, entry := got["7"].Sites["app.Svc.buy"], got["7"].Entry
	if site.N != 30 || site.Err != 30 || site.SumUs != 280000 || site.MaxUs != 16000 || site.SelfUs != 140000 {
		t.Fatalf("site = %+v", site)
	}
	if len(site.Errors) != 2 || site.Errors[0] != (measure.ErrorCount{Message: "boom", Count: 20}) || site.Errors[1].Count != 10 {
		t.Fatalf("errors = %+v", site.Errors)
	}
	if entry.N != 30 || entry.Spans != 120 || entry.Err != 0 || site.AvgMs() < 9.3 || site.P95Ms() != 16.383 {
		t.Fatalf("entry = %+v, avg %v p95 %v", entry, site.AvgMs(), site.P95Ms())
	}
	if ttl := mr.TTL(redisMeasureKey(measureBucket{"shop", "Deployment", "checkout", 1000})); ttl != redisMeasureKeyTTL {
		t.Fatalf("ttl = %v", ttl)
	}
}
