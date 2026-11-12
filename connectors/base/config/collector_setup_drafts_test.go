package config

import (
	"context"
	"testing"
)

type draftConfigStore struct{ saved map[string]any }

func (s *draftConfigStore) GetConfig(context.Context) (map[string]any, error) { return s.saved, nil }
func (s *draftConfigStore) SetConfig(_ context.Context, value map[string]any) error {
	s.saved = value
	return nil
}

func TestCollectorSetupDraftValidationKeepsBearerOutOfConfig(t *testing.T) {
	draft := map[string]any{"active": map[string]any{
		"form": map[string]any{"region": "us-east-2", "networkId": "vpc-123"},
		"mode": "manual", "method": "aws-cli", "stageId": "collector-manual-region",
		"updatedAt": "2026-10-13T12:00:00Z",
	}}
	if err := validateCollectorSetupDrafts(draft); err != nil {
		t.Fatalf("valid draft: %v", err)
	}
	draft["active"].(map[string]any)["authorizationHeader"] = "Bearer secret"
	if err := validateCollectorSetupDrafts(draft); err == nil {
		t.Fatal("Bearer field was accepted in durable config")
	}
}

func TestCollectorSetupDraftSurvivesManagerReload(t *testing.T) {
	ctx := context.Background()
	store := &draftConfigStore{saved: map[string]any{}}
	manager := NewManager(store, Defaults())
	drafts := map[string]any{"active": map[string]any{
		"form": map[string]any{"region": "us-east-2", "networkId": "vpc-123"},
		"mode": "manual", "method": "aws-cli", "stageId": "collector-manual-network",
		"updatedAt": "2026-10-13T12:00:00Z",
	}}
	if err := manager.Update(ctx, map[string]any{KeyCollectorSetupDrafts: drafts}); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	reloaded := NewManager(store, Defaults())
	if err := reloaded.Refresh(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Effective()[KeyCollectorSetupDrafts].(map[string]any)["active"].(map[string]any)["stageId"]; got != "collector-manual-network" {
		t.Fatalf("reloaded stage = %v", got)
	}
}
