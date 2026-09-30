package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/odigos-io/central-backend/graph/model"
)

// Single diagnose output — replaced on each new prepare (same model as OSS frontend).
var (
	diagnoseBytes    []byte
	diagnoseFilename string
	diagnoseLock     sync.RWMutex
)

// Agent GraphQL mutation (agents-api generateDiagnoseBundle). Kept inline so
// Central Backend does not depend on the TS apollo client package.
const generateDiagnoseBundleQuery = `
mutation GenerateDiagnoseBundle($includeContent: Boolean) {
  generateDiagnoseBundle(includeContent: $includeContent) {
    id
    filePath
    createdAt
    sizeBytes
    contentBase64
    error
  }
}
`

type agentDiagnoseBundle struct {
	ID            string   `json:"id"`
	FilePath      string   `json:"filePath"`
	CreatedAt     string   `json:"createdAt"`
	SizeBytes     *float64 `json:"sizeBytes"`
	ContentBase64 *string  `json:"contentBase64"`
	Error         *string  `json:"error"`
}

type agentDiagnoseEnvelope struct {
	Data   *struct {
		GenerateDiagnoseBundle *agentDiagnoseBundle `json:"generateDiagnoseBundle"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// RemoteFetchFunc matches graph.Query.RemoteFetch for server-side agent calls.
type RemoteFetchFunc func(ctx context.Context, proxyID string, query string, variables map[string]any) (string, error)

// DiagnoseGraphQL prepares a VM-agent diagnose archive via remoteFetch, caches
// the bytes for DiagnoseDownload, and returns kit-compatible stats. contentBase64
// never leaves Central Backend.
func DiagnoseGraphQL(
	ctx context.Context,
	proxyID string,
	input *model.DiagnoseInput,
	dryRun *bool,
	remoteFetch RemoteFetchFunc,
) (*model.DiagnoseResponse, error) {
	includeProfiles := true
	includeMetrics := true
	includeSourceWorkloads := false
	var sourceWorkloadNamespaces []string
	if input != nil {
		if input.IncludeProfiles != nil {
			includeProfiles = *input.IncludeProfiles
		}
		if input.IncludeMetrics != nil {
			includeMetrics = *input.IncludeMetrics
		}
		if input.IncludeSourceWorkloads != nil {
			includeSourceWorkloads = *input.IncludeSourceWorkloads
		}
		sourceWorkloadNamespaces = input.SourceWorkloadNamespaces
	}

	resp := &model.DiagnoseResponse{
		Stats: &model.DiagnoseStats{
			FileCount:      0,
			TotalSizeBytes: 0,
			TotalSizeHuman: formatDiagnoseBytes(0),
		},
		IncludeProfiles:          includeProfiles,
		IncludeMetrics:           includeMetrics,
		IncludeSourceWorkloads:   includeSourceWorkloads,
		SourceWorkloadNamespaces: sourceWorkloadNamespaces,
	}

	if dryRun != nil && *dryRun {
		return resp, nil
	}
	if proxyID == "" {
		return nil, fmt.Errorf("proxyID is required")
	}
	if remoteFetch == nil {
		return nil, fmt.Errorf("remote fetch is not configured")
	}

	raw, err := remoteFetch(ctx, proxyID, generateDiagnoseBundleQuery, map[string]any{
		"includeContent": true,
	})
	if err != nil {
		return nil, err
	}

	var envelope agentDiagnoseEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse agent diagnose response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("%s", envelope.Errors[0].Message)
	}
	if envelope.Data == nil || envelope.Data.GenerateDiagnoseBundle == nil {
		return nil, fmt.Errorf("agent returned no diagnose bundle")
	}
	bundle := envelope.Data.GenerateDiagnoseBundle
	if bundle.Error != nil && *bundle.Error != "" {
		return nil, fmt.Errorf("%s", *bundle.Error)
	}
	if bundle.ContentBase64 == nil || *bundle.ContentBase64 == "" {
		return nil, fmt.Errorf("agent did not return diagnose content for download")
	}

	content, err := base64.StdEncoding.DecodeString(*bundle.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("invalid diagnose content encoding: %w", err)
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("empty diagnose bundle")
	}

	filename := "odigos-diagnose.tar.gz"
	if bundle.FilePath != "" {
		if base := baseName(bundle.FilePath); base != "" {
			filename = base
		}
	}

	storeDiagnoseBundle(content, filename)

	sizeBytes := len(content)
	if bundle.SizeBytes != nil {
		sizeBytes = int(*bundle.SizeBytes)
	}
	resp.Stats = &model.DiagnoseStats{
		FileCount:      1,
		TotalSizeBytes: int32(sizeBytes),
		TotalSizeHuman: formatDiagnoseBytes(sizeBytes),
	}
	return resp, nil
}

// DiagnoseDownload streams the last prepared diagnose archive (OSS-compatible).
func DiagnoseDownload(c *gin.Context) {
	diagnoseLock.RLock()
	content := diagnoseBytes
	filename := diagnoseFilename
	diagnoseLock.RUnlock()

	if len(content) == 0 {
		c.JSON(404, gin.H{"error": "No diagnose output available. Run diagnose query first."})
		return
	}
	if filename == "" {
		filename = "odigos-diagnose.tar.gz"
	}

	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Header("Content-Length", fmt.Sprintf("%d", len(content)))
	c.Header("Cache-Control", "no-store")
	c.Data(200, "application/gzip", content)
}

func storeDiagnoseBundle(content []byte, filename string) {
	diagnoseLock.Lock()
	defer diagnoseLock.Unlock()
	diagnoseBytes = content
	diagnoseFilename = filename
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

func formatDiagnoseBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}
