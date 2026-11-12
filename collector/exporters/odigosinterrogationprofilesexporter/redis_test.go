package odigosinterrogationprofilesexporter

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pprofile"
	"go.uber.org/zap"
)

func TestInsertStacks_HSETNXAndExpire(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	// Keep TTL longer than the FastForward below so a no-op insert can prove EXPIRE is not refreshed.
	keyTTL := 5 * time.Minute
	pr := &profilesRedis{rdb: rdb, logger: zap.NewNop(), keyTTL: keyTTL}
	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{9, 8, 7, 6, 5, 4, 3, 2}
	stack := profileStack{
		TraceID:    traceID,
		SpanID:     spanID,
		SampleType: "samples",
		Frames: []profileFrame{
			{FunctionName: "com.app.Root.run", FrameType: "jvm"},
			{FunctionName: "com.app.Leaf.run", FrameType: "jvm"},
		},
	}

	require.NoError(t, pr.insertStacks(context.Background(), []profileStack{stack, stack}))

	key := redisProfileKey(traceID)
	require.True(t, mr.Exists(key))
	fields, err := rdb.HGetAll(context.Background(), key).Result()
	require.NoError(t, err)
	require.Len(t, fields, 1, "identical stacks of a span must share one immutable field")

	_, wantField, err := encodeStackField(&stack)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(wantField, hex.EncodeToString(spanID)+":"))
	raw, ok := fields[wantField]
	require.True(t, ok, "field %q missing; got %v", wantField, fields)

	var blob stackBlob
	require.NoError(t, json.Unmarshal([]byte(raw), &blob))
	assert.Equal(t, "samples", blob.SampleType)
	require.Len(t, blob.Frames, 2)
	assert.Equal(t, "com.app.Root.run", blob.Frames[0].FunctionName)
	assert.Equal(t, "jvm", blob.Frames[0].FrameType)

	ttl := mr.TTL(key)
	assert.Greater(t, ttl, time.Duration(0))
	assert.LessOrEqual(t, ttl, keyTTL)

	// Second insert of the same stack does not add fields and does not refresh TTL.
	beforeTTL := mr.TTL(key)
	mr.FastForward(time.Minute)
	require.NoError(t, pr.insertStacks(context.Background(), []profileStack{stack}))
	fields, err = rdb.HGetAll(context.Background(), key).Result()
	require.NoError(t, err)
	assert.Len(t, fields, 1)
	assert.Less(t, mr.TTL(key), beforeTTL)
}

func TestInsertStacks_DifferentStacksSameSpan(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	pr := &profilesRedis{rdb: rdb, logger: zap.NewNop(), keyTTL: 2 * defaultGroupByTraceWaitDuration}
	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	a := profileStack{
		TraceID: traceID, SpanID: spanID, SampleType: "samples",
		Frames: []profileFrame{{FunctionName: "com.app.A", FrameType: "jvm"}},
	}
	b := profileStack{
		TraceID: traceID, SpanID: spanID, SampleType: "samples",
		Frames: []profileFrame{{FunctionName: "com.app.B", FrameType: "jvm"}},
	}
	require.NoError(t, pr.insertStacks(context.Background(), []profileStack{a, b}))

	fields, err := rdb.HGetAll(context.Background(), redisProfileKey(traceID)).Result()
	require.NoError(t, err)
	assert.Len(t, fields, 2)
}

// A span's end event and its on-CPU samples arrive in the same profiler report.
// The event stack is written first and must not hide the samples.
func TestProfileStacksFrom_EventAndSamplesSameSpanSameReport(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	traceID := pcommon.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := pcommon.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	reportTime := pcommon.NewTimestampFromTime(time.Unix(100, 0))

	pd := pprofile.NewProfiles()
	dic := pd.Dictionary()
	strs := dic.StringTable()
	str := func(s string) int32 {
		strs.Append(s)
		return int32(strs.Len() - 1)
	}
	str("")
	dic.LinkTable().AppendEmpty()
	link := dic.LinkTable().AppendEmpty()
	link.SetTraceID(traceID)
	link.SetSpanID(spanID)
	dic.StackTable().AppendEmpty()
	dic.LocationTable().AppendEmpty()
	dic.FunctionTable().AppendEmpty()
	attrs := dic.AttributeTable()
	attrs.AppendEmpty()
	frameTypeAttr := attrs.AppendEmpty()
	frameTypeAttr.SetKeyStrindex(str(profileFrameTypeKey))
	frameTypeAttr.Value().SetStr("jvm")

	// stack builds a root-first Java stack and returns its stack-table index.
	stack := func(rootFirst ...string) int32 {
		st := dic.StackTable().AppendEmpty()
		for i := len(rootFirst) - 1; i >= 0; i-- {
			fn := dic.FunctionTable().AppendEmpty()
			fn.SetNameStrindex(str(rootFirst[i]))
			loc := dic.LocationTable().AppendEmpty()
			loc.AttributeIndices().Append(1)
			loc.Lines().AppendEmpty().SetFunctionIndex(int32(dic.FunctionTable().Len() - 1))
			st.LocationIndices().Append(int32(dic.LocationTable().Len() - 1))
		}
		return int32(dic.StackTable().Len() - 1)
	}
	eventStack := stack("void com.app.Api.handle()", "void com.app.Service.run()")
	cpuStackA := stack("void com.app.Api.handle()", "void com.app.Service.run()", "void com.app.Scorer.score()")
	cpuStackB := stack("void com.app.Api.handle()", "void com.app.Service.run()", "void com.app.Renderer.render()")

	sp := pd.ResourceProfiles().AppendEmpty().ScopeProfiles().AppendEmpty()
	addProfile := func(sampleType string, stacks ...int32) {
		prof := sp.Profiles().AppendEmpty()
		prof.SetTime(reportTime)
		prof.SampleType().SetTypeStrindex(str(sampleType))
		for _, s := range stacks {
			sample := prof.Samples().AppendEmpty()
			sample.SetStackIndex(s)
			sample.SetLinkIndex(1)
		}
	}
	addProfile("events", eventStack)
	addProfile("samples", cpuStackA, cpuStackB, cpuStackA)

	stacks := profileStacksFrom(pd)
	require.Len(t, stacks, 4)

	pr := &profilesRedis{rdb: rdb, logger: zap.NewNop(), keyTTL: 2 * defaultGroupByTraceWaitDuration}
	require.NoError(t, pr.insertStacks(context.Background(), stacks))

	fields, err := rdb.HGetAll(context.Background(), redisProfileKey(traceID[:])).Result()
	require.NoError(t, err)
	byType := map[string]int{}
	for _, raw := range fields {
		var blob stackBlob
		require.NoError(t, json.Unmarshal([]byte(raw), &blob))
		byType[blob.SampleType]++
	}
	assert.Equal(t, map[string]int{"events": 1, "samples": 2}, byType,
		"each distinct stack of the span is kept; the repeated on-CPU stack collapses")
}

func TestInsertStacks_DropsWhenTraceHashFull(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	pr := &profilesRedis{rdb: rdb, logger: zap.NewNop(), keyTTL: 2 * defaultGroupByTraceWaitDuration}
	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	stacks := make([]profileStack, 0, redisMaxStacksPerTrace+10)
	for i := 0; i < redisMaxStacksPerTrace+10; i++ {
		stacks = append(stacks, profileStack{
			TraceID:    traceID,
			SpanID:     spanID,
			SampleType: "samples",
			Frames:     []profileFrame{{FunctionName: fmt.Sprintf("com.app.F%d", i), FrameType: "jvm"}},
		})
	}
	require.NoError(t, pr.insertStacks(context.Background(), stacks))

	n, err := rdb.HLen(context.Background(), redisProfileKey(traceID)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(redisMaxStacksPerTrace), n)

	// Re-inserting an existing field is a no-op and must not evict or grow.
	require.NoError(t, pr.insertStacks(context.Background(), []profileStack{stacks[0]}))
	n, err = rdb.HLen(context.Background(), redisProfileKey(traceID)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(redisMaxStacksPerTrace), n)
}
