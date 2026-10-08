package store

import (
	"strings"
	"testing"
)

// The workload rows carry the network facts managed-Collector placement runs on: a Fargate service's
// awsvpc subnets and a Lambda's VpcConfig. Both live in the metadata JSONB
// rather than in columns of their own, which buys them a schema-free arrival — and costs them every
// compile-time check, because a SELECT list and a Scan argument list agree only at runtime and only
// against a real database.
//
// These tests are what stands in for that check. Neither needs a database.

// countAtTopLevel counts commas outside parentheses, which is the number of separators in a SQL column
// list or a Go argument list — one less than the number of items. Commas inside COALESCE(...) belong to
// the function call, not to the list.
func countAtTopLevel(s string) int {
	depth, n := 0, 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				n++
			}
		}
	}
	return n + 1
}

// TestLambdaSelectAndScanAgree. lambdaSelectCols is read by ListLambdaFunctions and GetLambdaFunction
// and consumed by one scanLambda, so a column added to the list without a matching target — or added in
// a different position — is a scan error on every read of the Lambda inventory, with the connector
// reporting no functions at all.
func TestLambdaSelectAndScanAgree(t *testing.T) {
	cols := countAtTopLevel(lambdaSelectCols)

	body := funcBody(t, readInventorySource(t), "func scanLambda")
	call := between(t, body, "row.Scan(", "\n\tif err != nil")
	args := countAtTopLevel(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(call), ")")))

	if cols != args {
		t.Fatalf("lambdaSelectCols selects %d columns but scanLambda scans %d targets", cols, args)
	}
}

// TestECSSelectAndScanAgree is the same guard over ListECSServices, whose SELECT is inline.
func TestECSSelectAndScanAgree(t *testing.T) {
	src := readECSSource(t)
	list := funcBody(t, src, "func (s *Store) ListECSServices")
	// The SELECT is a Go expression, not a plain literal: it splices the metadata-key constants (which
	// carry no commas and need no expansion) and ecsObservationCols, which is itself a five-column list.
	// Counting the raw source would undercount by those five.
	sel := strings.Replace(between(t, list, "SELECT ", "\n\t\tFROM"), "`+ecsObservationCols+`", ecsObservationCols, 1)
	if strings.Contains(sel, "ecsObservationCols") {
		t.Fatal("the observation columns are no longer spliced as `+ecsObservationCols+`; this test is undercounting")
	}
	cols := countAtTopLevel(sel)
	call := between(t, list, "rows.Scan(", "; err != nil")
	args := countAtTopLevel(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(call), ")")))

	if cols != args {
		t.Fatalf("ListECSServices selects %d columns but scans %d targets", cols, args)
	}
}

// TestLambdaVpcConfigKeysAreClearedByDiscovery. The lambda upsert overlays metadata after removing the
// keys discovery owns, so a key that is written but NOT listed as discovery-owned is never cleared.
// Detaching a function from its VPC removes VpcConfig entirely, and a vpc_id left behind would keep
// grouping that function onto a network it no longer runs on — pointing it at a Collector it can no
// longer reach, which says nothing re-points on health.
func TestLambdaVpcConfigKeysAreClearedByDiscovery(t *testing.T) {
	owned := map[string]bool{}
	for _, k := range lambdaDiscoveryMetadataKeys {
		owned[k] = true
	}
	for _, k := range []string{metaLambdaVPCID, metaLambdaSubnetIDs, metaLambdaSecurityGroupIDs} {
		if !owned[k] {
			t.Errorf("%q is written by discovery but not in lambdaDiscoveryMetadataKeys, so detaching a function from its VPC leaves it stale for ever", k)
		}
	}

	upsert := statementIn(t, readInventorySource(t), "func (s *Store) UpsertLambda")
	if !strings.Contains(upsert, "- $11::text[]") {
		t.Error("UpsertLambda no longer removes the discovery-owned keys before overlaying; every one of them is now write-only")
	}
}

// TestECSNetworkConfigurationIsAlwaysWritten. UpsertECSService MERGES metadata, so a key left out of a
// scan keeps the PREVIOUS scan's value: a service moved to different subnets would keep placing its
// Collector on the network it used to run on.
func TestECSNetworkConfigurationIsAlwaysWritten(t *testing.T) {
	body := funcBody(t, readECSSource(t), "func (s *Store) UpsertECSService")
	for _, k := range []string{"metaSubnetIDs", "metaSecurityGroupIDs"} {
		if !strings.Contains(body, "svc.Metadata["+k+"]") {
			t.Errorf("UpsertECSService does not write %s unconditionally; a stale value survives the scan that dropped it", k)
		}
	}
	if strings.Contains(body, "if len(svc.SubnetIDs)") {
		t.Error("UpsertECSService writes the subnets conditionally, which is what leaves a stale value behind")
	}
}

// funcBody returns the source of the named function.
func funcBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("%s not found; this test has stopped checking anything", decl)
	}
	body := src[i+len(decl):]
	if j := strings.Index(body, "\nfunc "); j >= 0 {
		body = body[:j]
	}
	return body
}

// between returns the text between the first open and the following close marker.
func between(t *testing.T, src, open, close string) string {
	t.Helper()
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatalf("%q not found; this test has stopped checking anything", open)
	}
	rest := src[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		t.Fatalf("%q not found after %q; this test has stopped checking anything", close, open)
	}
	return rest[:j]
}
