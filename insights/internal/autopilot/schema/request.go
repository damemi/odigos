package schema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RequestVersion is the AnalysisRequest schema generation.
const RequestVersion = "4"

// AnalysisRequest is the user-message payload. It is evidence only.
type AnalysisRequest struct {
	SchemaVersion  string            `json:"schemaVersion"`
	Service        Service           `json:"service"`
	ExistingProbes []ExistingProbe   `json:"existingProbes"`
	Transaction    RequestTxn        `json:"transaction"`
	EvidenceMode   string            `json:"evidenceMode,omitempty"` // "full" or "delta"
	CallGraph      []RequestCallEdge `json:"callGraph,omitempty"`
	AddedCallGraph []RequestCallEdge `json:"addedCallGraph,omitempty"`
}

// Service is the instrumented workload container.
type Service struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Container string `json:"container"`
}

// ExistingProbe is a custom probe that already applies to the service.
type ExistingProbe struct {
	Language string `json:"language"`
	Target   string `json:"target"`
	RuleName string `json:"ruleName"`
}

// RequestTxn is the observed transaction identity.
type RequestTxn struct {
	ID           string `json:"id"`
	RootSpanName string `json:"rootSpanName"`
	RootSpanKind string `json:"rootSpanKind"`
}

// RequestCallEdge is a merged caller→callee link between application functions.
type RequestCallEdge struct {
	Caller string `json:"caller"`
	Callee string `json:"callee"`
}

// NewFullEvidenceRequest builds a full evidence payload (first analysis or fallback).
func NewFullEvidenceRequest(svc Service, probes []ExistingProbe, tx RequestTxn, graph []RequestCallEdge) AnalysisRequest {
	return AnalysisRequest{
		SchemaVersion:  RequestVersion,
		Service:        svc,
		ExistingProbes: normalizeProbes(probes),
		Transaction:    tx,
		EvidenceMode:   "full",
		CallGraph:      sortCallGraph(graph),
	}
}

// NewDeltaEvidenceRequest sends only net-new edges since the prior snapshot.
func NewDeltaEvidenceRequest(svc Service, probes []ExistingProbe, tx RequestTxn, addedGraph []RequestCallEdge) AnalysisRequest {
	return AnalysisRequest{
		SchemaVersion:  RequestVersion,
		Service:        svc,
		ExistingProbes: normalizeProbes(probes),
		Transaction:    tx,
		EvidenceMode:   "delta",
		AddedCallGraph: sortCallGraph(addedGraph),
	}
}

func normalizeProbes(probes []ExistingProbe) []ExistingProbe {
	if probes == nil {
		return []ExistingProbe{}
	}
	return probes
}

func sortCallGraph(in []RequestCallEdge) []RequestCallEdge {
	cp := append([]RequestCallEdge(nil), in...)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].Caller != cp[j].Caller {
			return cp[i].Caller < cp[j].Caller
		}
		return cp[i].Callee < cp[j].Callee
	})
	return cp
}

// MarshalUserMessage encodes the request as compact JSON for the user role.
func MarshalUserMessage(req AnalysisRequest) (string, error) {
	if strings.TrimSpace(req.SchemaVersion) == "" {
		req.SchemaVersion = RequestVersion
	}
	if req.ExistingProbes == nil {
		req.ExistingProbes = []ExistingProbe{}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal analysis request: %w", err)
	}
	return string(raw), nil
}
