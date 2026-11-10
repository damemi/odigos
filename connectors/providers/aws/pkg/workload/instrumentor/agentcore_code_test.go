package instrumentor

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/odigos-io/odigos/connectors/base/api"
)

// agentCoreS3Object serves one object's bytes the way S3 does: HEAD for the size, GET for a byte range.
type agentCoreS3Object struct {
	data     []byte
	mu       sync.Mutex
	gets     int
	fullGets int
}

func (o *agentCoreS3Object) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(o.data)))}, nil
}

func (o *agentCoreS3Object) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.gets++
	if in.Range == nil {
		o.fullGets++
		return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(o.data))}, nil
	}
	spec, ok := strings.CutPrefix(aws.ToString(in.Range), "bytes=")
	from, to, found := strings.Cut(spec, "-")
	start, err1 := strconv.ParseInt(from, 10, 64)
	stop, err2 := strconv.ParseInt(to, 10, 64)
	if !ok || !found || err1 != nil || err2 != nil || start > stop || stop >= int64(len(o.data)) {
		return nil, fmt.Errorf("bad range %q", aws.ToString(in.Range))
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(o.data[start : stop+1]))}, nil
}

// testAgentCoreLargeZIP is a dependency-heavy agent: thousands of small members, incompressible payloads far
// larger than the central directory, and the few members detection opens.
func testAgentCoreLargeZIP(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	payload := make([]byte, 64<<10)
	for i := range 400 {
		if _, err := rand.Read(payload); err != nil {
			t.Fatal(err)
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("site/pkg%03d/blob.bin", i), Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(payload); err != nil {
			t.Fatal(err)
		}
		for j := range 10 {
			if _, err := w.Create(fmt.Sprintf("site/pkg%03d/module%02d.py", i, j)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, body := range extra {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestAgentCoreDetectionReadsOnlyTheZIPIndex: detection over ranged reads reaches the same facts as over the whole
// archive, while fetching a small fraction of it.
func TestAgentCoreDetectionReadsOnlyTheZIPIndex(t *testing.T) {
	data := testAgentCoreLargeZIP(t, map[string]string{
		"app.py":             "",
		agentCorePythonStart: agentCoreTestWrapper,
		"odigos_agentcore-0.1.0.dist-info/METADATA":          "",
		"aws_opentelemetry_distro-0.10.0.dist-info/METADATA": "",
	})
	object := &agentCoreS3Object{data: data}
	code, err := openAgentCoreS3Code(context.Background(), object, agentCoreArtifactRef{Bucket: "code", Key: "agent.zip", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := detectAgentCoreZIP(code, "python", "3.12", []string{"app.py"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := detectAgentCoreZIP(bytes.NewReader(data), "python", "3.12", []string{"app.py"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || got.OdigosPackage == nil || !got.StartWrapperReadsEnv {
		t.Fatalf("ranged detection differs:\n got %+v\nwant %+v", got, want)
	}
	fetched, requests := code.stats()
	if object.fullGets != 0 || fetched > int64(len(data))/4 || requests > 8 {
		t.Fatalf("detection fetched %d of %d bytes in %d requests (%d whole-object reads)", fetched, len(data), requests, object.fullGets)
	}
}

func TestAgentCoreS3CodeReadsAcrossBlocks(t *testing.T) {
	data := make([]byte, 3*agentCoreCodeBlock+123)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	object := &agentCoreS3Object{data: data}
	code, err := openAgentCoreS3Code(context.Background(), object, agentCoreArtifactRef{Bucket: "code", Key: "agent.zip"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ off, n int64 }{
		{0, 10}, {agentCoreCodeBlock - 5, 10}, {2*agentCoreCodeBlock - 1, agentCoreCodeBlock + 2}, {int64(len(data)) - 7, 7},
	} {
		p := make([]byte, tc.n)
		if n, err := code.ReadAt(p, tc.off); err != nil || int64(n) != tc.n || !bytes.Equal(p, data[tc.off:tc.off+tc.n]) {
			t.Fatalf("ReadAt(%d, %d) = %d, %v", tc.off, tc.n, n, err)
		}
	}
	// Past the end: a short read reports EOF, and reads at the end report EOF alone.
	p := make([]byte, 20)
	if n, err := code.ReadAt(p, int64(len(data))-10); n != 10 || err != io.EOF || !bytes.Equal(p[:10], data[len(data)-10:]) {
		t.Fatalf("short read = %d, %v", n, err)
	}
	if n, err := code.ReadAt(p, int64(len(data))); n != 0 || err != io.EOF {
		t.Fatalf("read at end = %d, %v", n, err)
	}
	// Every block was fetched once: reads that hit cached blocks make no further requests.
	if _, requests := code.stats(); requests != object.gets || requests > 4 {
		t.Fatalf("requests = %d, gets = %d", requests, object.gets)
	}
}

// TestAgentCoreBudgetHoldsUnderConcurrency: runtimes processed at once never take more updates than the pass
// allows, and a runtime that ends unchanged gives its reservation back.
func TestAgentCoreBudgetHoldsUnderConcurrency(t *testing.T) {
	budget := &agentCoreBudget{left: 5}
	var mu sync.Mutex
	taken := 0
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			s := budget.slot()
			defer s.release()
			first := s.take()
			again := s.take() // a second take within one runtime reuses its reservation
			if first != again {
				t.Error("a second take within one runtime did not reuse its reservation")
			}
			if first {
				mu.Lock()
				taken++
				mu.Unlock()
				s.spend()
			}
		})
	}
	wg.Wait()
	if taken != 5 || budget.remaining() != 0 {
		t.Fatalf("taken = %d, remaining = %d", taken, budget.remaining())
	}
	unchanged := &agentCoreBudget{left: 1}
	s := unchanged.slot()
	if !s.take() {
		t.Fatal("first reservation refused")
	}
	s.release()
	if unchanged.remaining() != 1 {
		t.Fatal("an unspent reservation was not returned")
	}
	if (&agentCoreSlot{}).take() {
		t.Fatal("a slot without a budget may change a runtime")
	}
}

// TestAgentCoreConfirmsDefaultInTheSamePass: once DEFAULT serves the update, the runtime reads as instrumented
// without waiting for the next pass. When it does not settle in time, it keeps settling.
func TestAgentCoreConfirmsDefaultInTheSamePass(t *testing.T) {
	r, s, f := newAgentCoreTest("python")
	r.confirmFor, r.confirmEvery = time.Second, 10*time.Millisecond
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 1 || s.state["instrumentation_phase"] != api.PhaseInstrumented || s.state["settling"] != false {
		t.Fatalf("not confirmed in the same pass: updates=%d state=%v", len(f.updates), s.state)
	}

	r, s, f = newAgentCoreTest("python")
	r.confirmFor, r.confirmEvery = 50*time.Millisecond, 10*time.Millisecond
	f.defaultVersion = "1" // DEFAULT keeps serving the version before the update
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 1 || s.state["instrumentation_phase"] != api.PhaseInstrumenting || s.state["settling"] != true {
		t.Fatalf("an unsettled runtime read as confirmed: updates=%d state=%v", len(f.updates), s.state)
	}
}
