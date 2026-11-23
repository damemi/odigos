package interrogation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	"github.com/odigos-io/odigos/insights/internal/autopilot/measure"
	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestBuildRequest(t *testing.T) {
	source := store.SourceKey{Namespace: "claims-demo", WorkloadKind: "Deployment", WorkloadName: "claims-intake", ContainerName: "app"}
	probes := []loop.CustomProbe{{RuleName: "odig-int-claims-demo-claims-intake", Language: "java", Target: "com.claims.IntakeService.validate"}}
	checkout := loop.BuildFullAnalysisRequest(source, probes,
		loop.TransactionCallTree{Transaction: store.Transaction{ID: "8841", RootSpanName: "POST /claims", RootSpanKind: "server"}},
		loop.AppEvidence{CallGraph: []loop.CallGraphEdge{
			{Caller: "com.claims.IntakeService.validate", Callee: "com.claims.FraudClient.score"},
			{Caller: "com.claims.ClaimController.submit", Callee: "com.claims.IntakeService.validate"},
			{Caller: "com.claims.ClaimController.submit", Callee: "com.claims.ClaimRepository.save"},
		}})
	source.ContainerName = "worker"
	idle := loop.BuildFullAnalysisRequest(source, probes,
		loop.TransactionCallTree{Transaction: store.Transaction{ID: "8850", RootSpanName: "claims.retry", RootSpanKind: "consumer"}},
		loop.AppEvidence{})

	at := time.Date(2026, 11, 17, 9, 40, 0, 0, time.UTC)
	st := &State{
		Round: 2,
		Request: repo.InterrogationRequest{
			Source:         repo.InterrogationSource{Namespace: "claims-demo", Kind: "Deployment", Name: "claims-intake"},
			Goal:           "observability",
			Question:       "Why is checkout slow?",
			TransactionIDs: []int64{8841},
			AllowChanges:   []string{"custom_instrumentation"},
			Budget:         repo.InterrogationBudget{MaxProbes: 5, TimeLimitSeconds: 3600, MaxRounds: 5},
		},
		Probes: []Probe{
			{Language: "java", Target: "com.claims.IntakeService.validate", Round: 1, Status: ProbeApplied, Measured: &Measured{
				WindowSeconds: 30, MeasuredAt: at, Spans: 41, Errors: 2, AvgMs: 12.5, P95Ms: 31, MaxMs: 48.25, SelfAvgMs: 9.1,
				TopErrors: []ErrorCount{{Message: "claim rejected", Count: 2}},
			}},
			{Language: "java", Target: "com.claims.ClaimRepository.save", Round: 1, Status: ProbeDenied},
			{Language: "java", Target: "com.claims.FraudClient.score", Round: 1, Status: ProbeFailed},
			{Language: "java", Target: "com.claims.Audit.write", Round: 1, Status: ProbeReverted},
			{Language: "java", Target: "com.claims.Next.one", Round: 2, Status: ProbePending},
		},
		Answer: &Answer{Text: "Most of the time is in IntakeService.validate.", Certainty: 45, UpdatedAt: at},
	}

	timings := requestTimings(&measure.Transaction{
		Entry: measure.Measure{N: 40, Err: 1, SumUs: 800000, MaxUs: 90000, SelfUs: 80000, Spans: 160},
		Sites: map[string]*measure.Measure{"com.claims.IntakeService.validate": {N: 41, Err: 2, SumUs: 512500, MaxUs: 48250, SelfUs: 373100}},
	}, 30*time.Second)
	got, err := json.Marshal(buildRequest(st, []transactionEvidence{{Request: checkout, Changed: true, Timings: timings}, {Request: idle}}))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "request.json", got)
}
