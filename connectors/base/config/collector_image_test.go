package config

import (
	"context"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
)

func TestCollectorImageSettingsValidationAndPersistence(t *testing.T) {
	for _, location := range []string{"defaults", "regions"} {
		t.Run(location, func(t *testing.T) {
			store := &draftConfigStore{saved: map[string]any{}}
			manager := NewManager(store, Defaults())
			patch := func(image any) map[string]any {
				settings := map[string]any{"image": image}
				if location == "regions" {
					settings = map[string]any{"us-east-1": settings}
				}
				return map[string]any{"collector": map[string]any{location: settings}}
			}
			for _, bad := range []any{"not-an-object", map[string]any{"digest": "bad"}, map[string]any{"tag": 5}, map[string]any{"reference": "registry.example.com/collector"}} {
				if err := manager.Update(context.Background(), patch(bad)); err == nil {
					t.Fatalf("accepted image %#v", bad)
				}
			}
			if err := manager.Update(context.Background(), patch(map[string]any{"tag": "v2", "registry": "mirror.example.com"})); err != nil {
				t.Fatal(err)
			}
			reloaded := NewManager(store, Defaults())
			if err := reloaded.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			image := collectorsettings.Read(reloaded.Effective()).Region("us-east-1").Image
			if image["tag"] != "v2" || image["registry"] != "mirror.example.com" {
				t.Fatalf("persisted image = %#v", image)
			}
		})
	}
}
