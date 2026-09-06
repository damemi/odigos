package services

import (
	"context"
	"testing"

	"github.com/odigos-io/central-backend/internal/consts"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type fakeProxyMetaReader struct {
	values redisclient.RedisHashFieldValuesByKey
	err    error
}

func (f fakeProxyMetaReader) GetProxyMetaKeyValues(context.Context) (redisclient.RedisHashFieldValuesByKey, error) {
	return f.values, f.err
}

func TestExtractProxyIDFromProxyMetaKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "current two-part key format",
			key:  "proxy-meta:proxy-1",
			want: "proxy-1",
		},
		{
			name: "legacy multi-part key format",
			key:  "proxy-meta:backend-a:proxy-2",
			want: "proxy-2",
		},
		{
			name: "single part key is invalid",
			key:  "proxy-meta",
			want: "",
		},
		{
			name: "empty proxy id is invalid",
			key:  "proxy-meta:",
			want: "",
		},
		{
			name: "empty string key is invalid",
			key:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractProxyIDFromProxyMetaKey(tt.key)
			if got != tt.want {
				t.Fatalf("extractProxyIDFromProxyMetaKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestGetAllProxyMetadata_ParsesTwoPartKeysAndSkipsMalformedKeys(t *testing.T) {
	ctx := context.Background()

	svc := &ComputePlatformService{
		redis: fakeProxyMetaReader{
			values: redisclient.RedisHashFieldValuesByKey{
				"proxy-meta:proxy-two-part": {
					consts.ProxyMetaFieldPlatformName:  "platform-a",
					consts.ProxyMetaFieldPlatformType:  string(consts.PlatformTypeVM),
					consts.ProxyMetaFieldOdigosVersion: "1.2.3",
					consts.ProxyMetaFieldConnectedAt:   "1712345",
					consts.ProxyMetaFieldLastSeenAt:    "invalid-timestamp",
					consts.ProxyMetaFieldStatus:        string(consts.StatusConnected),
				},
				"proxy-meta:backend-a:proxy-three-part": {
					consts.ProxyMetaFieldPlatformName: "platform-b",
					consts.ProxyMetaFieldPlatformType: string(consts.PlatformTypeK8s),
					consts.ProxyMetaFieldStatus:       string(consts.StatusDisconnected),
				},
				"proxy-meta:": {
					consts.ProxyMetaFieldPlatformName: "malformed",
				},
				"proxy-meta": {
					consts.ProxyMetaFieldPlatformName: "also-malformed",
				},
			},
		},
	}

	metaByProxyID, err := svc.GetAllProxyMetadata(ctx)
	if err != nil {
		t.Fatalf("GetAllProxyMetadata returned error: %v", err)
	}

	if len(metaByProxyID) != 2 {
		t.Fatalf("expected 2 proxy entries, got %d", len(metaByProxyID))
	}

	twoPartMeta, ok := metaByProxyID["proxy-two-part"]
	if !ok {
		t.Fatalf("expected two-part key proxy metadata to be present")
	}
	if twoPartMeta.PlatformName != "platform-a" {
		t.Fatalf("expected platform name platform-a, got %q", twoPartMeta.PlatformName)
	}
	if twoPartMeta.PlatformType != string(consts.PlatformTypeVM) {
		t.Fatalf("expected platform type %q, got %q", consts.PlatformTypeVM, twoPartMeta.PlatformType)
	}
	if twoPartMeta.ConnectedAt != 1712345 {
		t.Fatalf("expected connected_at 1712345, got %d", twoPartMeta.ConnectedAt)
	}
	if twoPartMeta.LastSeenAt != 0 {
		t.Fatalf("expected invalid last_seen_at to remain 0, got %d", twoPartMeta.LastSeenAt)
	}

	if _, ok := metaByProxyID["proxy-three-part"]; !ok {
		t.Fatalf("expected legacy three-part key proxy metadata to be present")
	}
	if _, ok := metaByProxyID[""]; ok {
		t.Fatalf("did not expect metadata entry with empty proxy id")
	}
}

func TestGetAllProxyMetadata_PropagatesReaderErrors(t *testing.T) {
	ctx := context.Background()
	expectedErr := context.DeadlineExceeded

	svc := &ComputePlatformService{
		redis: fakeProxyMetaReader{
			err: expectedErr,
		},
	}

	metaByProxyID, err := svc.GetAllProxyMetadata(ctx)
	if err == nil {
		t.Fatalf("expected GetAllProxyMetadata to return an error")
	}
	if err != expectedErr {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
	if metaByProxyID != nil {
		t.Fatalf("expected nil metadata map when reader returns error")
	}
}
