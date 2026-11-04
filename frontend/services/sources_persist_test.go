package services

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	odigosfake "github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned/fake"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testNs     = "shop"
	testStream = "default"
)

// sourceRecorder keeps the state of every Source after each create or patch, in the order they were applied.
type sourceRecorder struct {
	mu        sync.Mutex
	client    *odigosfake.Clientset
	snapshots []sourceSnapshot
	creates   []string
	defaulted []string
	generated int
}

type sourceSnapshot struct {
	verb     string
	workload string
	disabled bool
	labels   map[string]string
}

func workloadID(kind k8sconsts.WorkloadKind, name string) string {
	return fmt.Sprintf("%s/%s", kind, name)
}

// applyDataStreamDefault does what the instrumentor's Source defaulting webhook does on create and update:
// a source without a "true" data-stream label is added to the default stream.
func applyDataStreamDefault(source *v1alpha1.Source) bool {
	for labelKey, labelValue := range source.Labels {
		if strings.HasPrefix(labelKey, k8sconsts.SourceDataStreamLabelPrefix) && labelValue == "true" {
			return false
		}
	}
	if source.Labels == nil {
		source.Labels = map[string]string{}
	}
	source.Labels[k8sconsts.SourceDataStreamLabelPrefix+testStream] = "true"
	return true
}

// setupFakePersistClient points kube.DefaultClient at a fake odigos clientset holding sources.
// Created sources get a name and the workload labels that the API server webhook would set, and writes get
// the webhook's data-stream defaulting. A test fails if any write needed that defaulting.
func setupFakePersistClient(t *testing.T, sources ...*v1alpha1.Source) *sourceRecorder {
	t.Helper()

	objects := []runtime.Object{}
	for _, source := range sources {
		objects = append(objects, source)
	}
	recorder := &sourceRecorder{client: odigosfake.NewSimpleClientset(objects...)} //nolint:staticcheck // NewClientset can't type Source objects without generated apply configurations

	recorder.client.PrependReactor("*", "sources", func(action k8stesting.Action) (bool, runtime.Object, error) {
		verb := action.GetVerb()
		if verb != "create" && verb != "patch" {
			return false, nil, nil
		}
		if verb == "create" {
			source := action.(k8stesting.CreateAction).GetObject().(*v1alpha1.Source)
			recorder.mu.Lock()
			recorder.generated++
			source.Name = fmt.Sprintf("%s%d", source.GenerateName, recorder.generated)
			recorder.mu.Unlock()
			if source.Labels == nil {
				source.Labels = map[string]string{}
			}
			source.Labels[k8sconsts.WorkloadNamespaceLabel] = source.Spec.Workload.Namespace
			source.Labels[k8sconsts.WorkloadNameLabel] = source.Spec.Workload.Name
			source.Labels[k8sconsts.WorkloadKindLabel] = string(source.Spec.Workload.Kind)
			if applyDataStreamDefault(source) {
				recorder.mu.Lock()
				recorder.defaulted = append(recorder.defaulted, workloadID(source.Spec.Workload.Kind, source.Spec.Workload.Name))
				recorder.mu.Unlock()
			}
		}
		handled, obj, err := k8stesting.ObjectReaction(recorder.client.Tracker())(action)
		if err == nil {
			source := obj.(*v1alpha1.Source)
			id := workloadID(source.Spec.Workload.Kind, source.Spec.Workload.Name)
			if verb == "patch" && applyDataStreamDefault(source) {
				recorder.mu.Lock()
				recorder.defaulted = append(recorder.defaulted, id)
				recorder.mu.Unlock()
				err = recorder.client.Tracker().Update(v1alpha1.SchemeGroupVersion.WithResource("sources"), source, source.Namespace)
			}
			labels := map[string]string{}
			for key, value := range source.Labels {
				labels[key] = value
			}
			recorder.mu.Lock()
			recorder.snapshots = append(recorder.snapshots, sourceSnapshot{verb: verb, workload: id, disabled: source.Spec.DisableInstrumentation, labels: labels})
			if verb == "create" {
				recorder.creates = append(recorder.creates, id)
			}
			recorder.mu.Unlock()
		}
		return handled, obj, err
	})

	previous := kube.DefaultClient
	kube.SetDefaultClient(&kube.Client{OdigosClient: recorder.client.OdigosV1alpha1()})
	t.Cleanup(func() {
		kube.SetDefaultClient(previous)
		assert.Empty(t, recorder.defaulted, "the instrumentor webhook would rewrite the data-stream labels of these sources")
	})

	return recorder
}

func existingSource(name string, kind k8sconsts.WorkloadKind, workload string, disabled bool, streams map[string]string) *v1alpha1.Source {
	labels := map[string]string{
		k8sconsts.WorkloadNamespaceLabel: testNs,
		k8sconsts.WorkloadNameLabel:      workload,
		k8sconsts.WorkloadKindLabel:      string(kind),
	}
	for stream, value := range streams {
		labels[k8sconsts.SourceDataStreamLabelPrefix+stream] = value
	}
	return &v1alpha1.Source{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs, Labels: labels},
		Spec: v1alpha1.SourceSpec{
			Workload:               k8sconsts.PodWorkload{Namespace: testNs, Name: workload, Kind: kind},
			DisableInstrumentation: disabled,
		},
	}
}

func persistWorkload(name string, selected bool) *model.PersistNamespaceSourceInput {
	return &model.PersistNamespaceSourceInput{Namespace: testNs, Name: name, Kind: WorkloadKindDeployment, Selected: selected, CurrentStreamName: testStream}
}

func disableWorkload(name string, disable bool) *model.PersistNamespaceSourceInput {
	workload := persistWorkload(name, false)
	workload.DisableInstrumentation = &disable
	return workload
}

func persistNamespace(selected bool) *model.PersistNamespaceSourceInput {
	return &model.PersistNamespaceSourceInput{Namespace: testNs, Name: testNs, Kind: WorkloadKindNamespace, Selected: selected, CurrentStreamName: testStream}
}

func getSource(t *testing.T, kind model.K8sResourceKind, name string) *v1alpha1.Source {
	t.Helper()
	source, err := GetSourceCRD(context.Background(), testNs, name, kind)
	require.NoError(t, err)
	return source
}

func streamLabel(source *v1alpha1.Source, stream string) string {
	return source.Labels[k8sconsts.SourceDataStreamLabelPrefix+stream]
}

func TestSyncWorkloadsInNamespace_ExcludedWorkloadCreatedBeforeNamespace(t *testing.T) {
	recorder := setupFakePersistClient(t)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		persistNamespace(true),
		persistWorkload("cart", false),
		persistWorkload("checkout", true),
	})
	require.NoError(t, err)

	nsID := workloadID(k8sconsts.WorkloadKindNamespace, testNs)
	require.Len(t, recorder.creates, 3)
	assert.Equal(t, nsID, recorder.creates[2], "the namespace source must be created after every workload source")

	nsSource := getSource(t, WorkloadKindNamespace, testNs)
	assert.False(t, nsSource.Spec.DisableInstrumentation)
	assert.Equal(t, "true", streamLabel(nsSource, testStream))

	excluded := getSource(t, WorkloadKindDeployment, "cart")
	assert.True(t, excluded.Spec.DisableInstrumentation)
	assert.Equal(t, "true", streamLabel(excluded, testStream))

	selected := getSource(t, WorkloadKindDeployment, "checkout")
	assert.False(t, selected.Spec.DisableInstrumentation)
}

func TestSyncWorkloadsInNamespace_ExcludedWorkloadIsNeverEnabled(t *testing.T) {
	recorder := setupFakePersistClient(t)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		persistWorkload("cart", false),
		persistNamespace(true),
	})
	require.NoError(t, err)

	cartID := workloadID(k8sconsts.WorkloadKindDeployment, "cart")
	seen := 0
	for _, snapshot := range recorder.snapshots {
		if snapshot.workload != cartID {
			continue
		}
		seen++
		assert.True(t, snapshot.disabled, "%s of the excluded source must keep instrumentation disabled", snapshot.verb)
		assert.Equal(t, "true", snapshot.labels[k8sconsts.SourceDataStreamLabelPrefix+testStream])
	}
	assert.Equal(t, 1, seen, "the excluded source must be created once, with no follow-up patches")
}

func TestSyncWorkloadsInNamespace_ExistingNamespaceSourceCreatesDisabledSource(t *testing.T) {
	recorder := setupFakePersistClient(t, existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{persistWorkload("cart", false)})
	require.NoError(t, err)

	require.Len(t, recorder.snapshots, 1)
	assert.Equal(t, "create", recorder.snapshots[0].verb)
	assert.True(t, recorder.snapshots[0].disabled)

	excluded := getSource(t, WorkloadKindDeployment, "cart")
	assert.True(t, excluded.Spec.DisableInstrumentation)
	assert.Equal(t, "true", streamLabel(excluded, testStream))
}

func TestSyncWorkloadsInNamespace_AlreadyExcludedWorkloadIsNotReEnabled(t *testing.T) {
	recorder := setupFakePersistClient(t,
		existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true"}),
		existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", true, map[string]string{testStream: "false"}),
	)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		persistWorkload("cart", false),
		persistNamespace(true),
	})
	require.NoError(t, err)

	for _, snapshot := range recorder.snapshots {
		if snapshot.workload == workloadID(k8sconsts.WorkloadKindDeployment, "cart") {
			assert.True(t, snapshot.disabled, "%s of the excluded source must keep instrumentation disabled", snapshot.verb)
		}
	}
	excluded := getSource(t, WorkloadKindDeployment, "cart")
	assert.True(t, excluded.Spec.DisableInstrumentation)
	assert.Equal(t, "false", streamLabel(excluded, testStream))
}

func TestSyncWorkloadsInNamespace_ReselectingReEnables(t *testing.T) {
	setupFakePersistClient(t,
		existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true"}),
		existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", true, map[string]string{testStream: "false"}),
	)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{persistWorkload("cart", true)})
	require.NoError(t, err)

	source := getSource(t, WorkloadKindDeployment, "cart")
	assert.False(t, source.Spec.DisableInstrumentation)
	assert.Equal(t, "true", streamLabel(source, testStream))
}

func TestSyncWorkloadsInNamespace_UnselectedWithoutNamespaceLeavesNoSource(t *testing.T) {
	recorder := setupFakePersistClient(t, existingSource("api-source", k8sconsts.WorkloadKindDeployment, "api", false, map[string]string{testStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		persistWorkload("api", false),
		persistWorkload("cart", false),
	})
	require.NoError(t, err)

	assert.Empty(t, recorder.creates, "no source is created for a workload without a namespace source")
	list, err := recorder.client.OdigosV1alpha1().Sources(testNs).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, list.Items)
}

func TestSyncWorkloadsInNamespace_NamespaceTurnedOffKeepsDisabledWorkloadSource(t *testing.T) {
	recorder := setupFakePersistClient(t,
		existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true"}),
		existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", true, map[string]string{testStream: "false"}),
		existingSource("api-source", k8sconsts.WorkloadKindDeployment, "api", false, map[string]string{testStream: "true"}),
	)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		persistWorkload("cart", false),
		persistWorkload("api", false),
		persistWorkload("checkout", true),
		persistNamespace(false),
	})
	require.NoError(t, err)

	names := map[string]bool{}
	for _, source := range listSources(t, recorder) {
		names[source.Spec.Workload.Name] = source.Spec.DisableInstrumentation
	}
	assert.Equal(t, map[string]bool{"cart": true, "checkout": false}, names, "the disabled source stays, the deselected enabled source is deleted")
}

func streamsOf(t *testing.T, name string) []string {
	t.Helper()
	streams := []string{}
	for _, stream := range ExtractDataStreamsFromSource(getSource(t, WorkloadKindDeployment, name), getSource(t, WorkloadKindNamespace, testNs)) {
		streams = append(streams, *stream)
	}
	return streams
}

func TestSyncWorkloadsInNamespace_ExcludedFromOneOfManyStreams(t *testing.T) {
	const otherStream = "payments"
	recorder := setupFakePersistClient(t, existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true", otherStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{persistWorkload("cart", false)})
	require.NoError(t, err)

	require.Len(t, recorder.snapshots, 1, "the source is created in its final state")
	excluded := getSource(t, WorkloadKindDeployment, "cart")
	assert.False(t, excluded.Spec.DisableInstrumentation, "the workload stays instrumented for the other stream")
	assert.Equal(t, "false", streamLabel(excluded, testStream))
	assert.Equal(t, "true", streamLabel(excluded, otherStream), "a remaining stream keeps the source valid for the webhook")
	assert.Equal(t, []string{otherStream}, streamsOf(t, "cart"))
}

func TestSyncWorkloadsInNamespace_ExcludedFromNonDefaultStream(t *testing.T) {
	const currentStream = "payments"
	recorder := setupFakePersistClient(t, existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true", currentStream: "true"}))

	workload := persistWorkload("cart", false)
	workload.CurrentStreamName = currentStream
	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{workload})
	require.NoError(t, err)

	require.Len(t, recorder.snapshots, 1)
	excluded := getSource(t, WorkloadKindDeployment, "cart")
	assert.False(t, excluded.Spec.DisableInstrumentation)
	assert.Equal(t, "false", streamLabel(excluded, currentStream))
	assert.Equal(t, []string{testStream}, streamsOf(t, "cart"))
}

func TestSyncWorkloadsInNamespace_ExistingSourceExcludedFromOneOfManyStreams(t *testing.T) {
	const otherStream = "payments"
	recorder := setupFakePersistClient(t,
		existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true", otherStream: "true"}),
		existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", false, map[string]string{testStream: "true"}),
	)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{persistWorkload("cart", false)})
	require.NoError(t, err)

	require.Len(t, recorder.snapshots, 1, "the labels are changed in one patch")
	excluded := getSource(t, WorkloadKindDeployment, "cart")
	assert.False(t, excluded.Spec.DisableInstrumentation)
	assert.Equal(t, "false", streamLabel(excluded, testStream))
	assert.Equal(t, "true", streamLabel(excluded, otherStream))
	assert.Equal(t, []string{otherStream}, streamsOf(t, "cart"))
}

func TestSyncWorkloadsInNamespace_ExistingSourceInManyStreamsKeepsOtherStreams(t *testing.T) {
	const otherStream = "payments"
	setupFakePersistClient(t, existingSource("api-source", k8sconsts.WorkloadKindDeployment, "api", false, map[string]string{testStream: "true", otherStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{persistWorkload("api", false)})
	require.NoError(t, err)

	source := getSource(t, WorkloadKindDeployment, "api")
	assert.False(t, source.Spec.DisableInstrumentation)
	_, hasCurrent := source.Labels[k8sconsts.SourceDataStreamLabelPrefix+testStream]
	assert.False(t, hasCurrent, "the current stream label is removed")
	assert.Equal(t, "true", streamLabel(source, otherStream))
}

func listSources(t *testing.T, recorder *sourceRecorder) []v1alpha1.Source {
	t.Helper()
	list, err := recorder.client.OdigosV1alpha1().Sources(testNs).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	return list.Items
}

// assertDisabledFromStart checks the workload source was created disabled, with a "true" data-stream label so the
// instrumentor webhook keeps its labels, and never changed afterwards.
func assertDisabledFromStart(t *testing.T, recorder *sourceRecorder, name string, stream string) {
	t.Helper()
	id := workloadID(k8sconsts.WorkloadKindDeployment, name)
	seen := 0
	for _, snapshot := range recorder.snapshots {
		if snapshot.workload != id {
			continue
		}
		seen++
		assert.Equal(t, "create", snapshot.verb)
		assert.True(t, snapshot.disabled, "the source must be created disabled")
		assert.Equal(t, "true", snapshot.labels[k8sconsts.SourceDataStreamLabelPrefix+stream])
	}
	assert.Equal(t, 1, seen, "the disabled source must be created once, with no follow-up patches")
}

func TestSyncWorkloadsInNamespace_DisableCreatesDisabledSourceWithoutNamespace(t *testing.T) {
	recorder := setupFakePersistClient(t)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{disableWorkload("cart", true)})
	require.NoError(t, err)

	assertDisabledFromStart(t, recorder, "cart", testStream)
	assert.True(t, getSource(t, WorkloadKindDeployment, "cart").Spec.DisableInstrumentation)
}

func TestSyncWorkloadsInNamespace_DisableWithNamespaceTurnedOnInSameRequest(t *testing.T) {
	recorder := setupFakePersistClient(t)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		persistNamespace(true),
		disableWorkload("cart", true),
		persistWorkload("checkout", true),
	})
	require.NoError(t, err)

	require.Len(t, recorder.creates, 3)
	assert.Equal(t, workloadID(k8sconsts.WorkloadKindNamespace, testNs), recorder.creates[2], "the namespace source must be created after every workload source")
	assertDisabledFromStart(t, recorder, "cart", testStream)
	assert.False(t, getSource(t, WorkloadKindDeployment, "checkout").Spec.DisableInstrumentation)
}

func TestSyncWorkloadsInNamespace_DisableKeepsSourceWhenNamespaceTurnedOff(t *testing.T) {
	recorder := setupFakePersistClient(t, existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		disableWorkload("cart", true),
		persistNamespace(false),
	})
	require.NoError(t, err)

	sources := listSources(t, recorder)
	require.Len(t, sources, 1, "the namespace source is deleted and the disabled workload source is kept")
	assert.Equal(t, "cart", sources[0].Spec.Workload.Name)
	assert.True(t, sources[0].Spec.DisableInstrumentation)
}

func TestSyncWorkloadsInNamespace_DisableInOtherStreamSetsOnlyThatStream(t *testing.T) {
	const otherStream = "payments"
	recorder := setupFakePersistClient(t)

	workload := disableWorkload("cart", true)
	workload.CurrentStreamName = otherStream
	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{workload})
	require.NoError(t, err)

	assertDisabledFromStart(t, recorder, "cart", otherStream)
	_, hasDefault := getSource(t, WorkloadKindDeployment, "cart").Labels[k8sconsts.SourceDataStreamLabelPrefix+testStream]
	assert.False(t, hasDefault, "the default stream label is not added")
}

func TestSyncWorkloadsInNamespace_DisableExistingEnabledSource(t *testing.T) {
	const otherStream = "payments"
	recorder := setupFakePersistClient(t, existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", false, map[string]string{testStream: "true", otherStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{disableWorkload("cart", true)})
	require.NoError(t, err)

	assert.Empty(t, recorder.creates)
	require.Len(t, recorder.snapshots, 1)
	assert.Equal(t, "patch", recorder.snapshots[0].verb)
	source := getSource(t, WorkloadKindDeployment, "cart")
	assert.Equal(t, "cart-source", source.Name)
	assert.True(t, source.Spec.DisableInstrumentation)
	assert.Equal(t, "true", streamLabel(source, testStream))
	assert.Equal(t, "true", streamLabel(source, otherStream))
}

func TestSyncWorkloadsInNamespace_DisableAlreadyDisabledSourceDoesNothing(t *testing.T) {
	recorder := setupFakePersistClient(t, existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", true, map[string]string{testStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{disableWorkload("cart", true)})
	require.NoError(t, err)

	assert.Empty(t, recorder.snapshots)
	assert.True(t, getSource(t, WorkloadKindDeployment, "cart").Spec.DisableInstrumentation)
}

func TestSyncWorkloadsInNamespace_UndisableDeletesDisabledSource(t *testing.T) {
	recorder := setupFakePersistClient(t,
		existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true"}),
		existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", true, map[string]string{testStream: "true"}),
	)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{disableWorkload("cart", false)})
	require.NoError(t, err)

	assert.Empty(t, recorder.snapshots, "the workload source is deleted without being enabled first")
	sources := listSources(t, recorder)
	require.Len(t, sources, 1, "the workload follows its namespace source again")
	assert.Equal(t, k8sconsts.WorkloadKindNamespace, sources[0].Spec.Workload.Kind)
}

func TestSyncWorkloadsInNamespace_UndisableWithoutSourceDoesNothing(t *testing.T) {
	recorder := setupFakePersistClient(t)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{disableWorkload("cart", false)})
	require.NoError(t, err)

	assert.Empty(t, recorder.snapshots)
	assert.Empty(t, listSources(t, recorder))
}

func TestSyncWorkloadsInNamespace_UndisableEnabledSourceKeepsOtherStreams(t *testing.T) {
	const otherStream = "payments"
	recorder := setupFakePersistClient(t, existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", false, map[string]string{testStream: "true", otherStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{disableWorkload("cart", false)})
	require.NoError(t, err)

	require.Len(t, listSources(t, recorder), 1)
	source := getSource(t, WorkloadKindDeployment, "cart")
	_, hasCurrent := source.Labels[k8sconsts.SourceDataStreamLabelPrefix+testStream]
	assert.False(t, hasCurrent, "the current stream label is removed")
	assert.Equal(t, "true", streamLabel(source, otherStream))
}

func TestSyncWorkloadsInNamespace_UnselectedEnabledSourceIsDisabledInPlace(t *testing.T) {
	recorder := setupFakePersistClient(t,
		existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true"}),
		existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", false, map[string]string{testStream: "true"}),
	)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{persistWorkload("cart", false)})
	require.NoError(t, err)

	require.Len(t, recorder.snapshots, 1, "only the disableInstrumentation flag is patched")
	assert.True(t, recorder.snapshots[0].disabled)
	assert.True(t, getSource(t, WorkloadKindDeployment, "cart").Spec.DisableInstrumentation)
}

func TestSyncWorkloadsInNamespace_DisabledNamespaceIsRejected(t *testing.T) {
	recorder := setupFakePersistClient(t)

	namespace := persistNamespace(false)
	disable := true
	namespace.DisableInstrumentation = &disable
	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{disableWorkload("cart", true), namespace})
	require.Error(t, err)

	assert.Empty(t, recorder.snapshots, "nothing is written when the request is rejected")
}

func TestSyncWorkloadsInNamespace_DeselectingDisabledSourceInManyStreamsDoesNothing(t *testing.T) {
	const otherStream = "payments"
	recorder := setupFakePersistClient(t,
		existingSource("ns-source", k8sconsts.WorkloadKindNamespace, testNs, false, map[string]string{testStream: "true", otherStream: "true"}),
		existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", true, map[string]string{testStream: "true"}),
		existingSource("api-source", k8sconsts.WorkloadKindDeployment, "api", true, map[string]string{testStream: "true", otherStream: "true"}),
	)

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		persistWorkload("cart", false),
		persistWorkload("api", false),
	})
	require.NoError(t, err)

	assert.Empty(t, recorder.snapshots)
	assert.True(t, getSource(t, WorkloadKindDeployment, "cart").Spec.DisableInstrumentation)
	assert.True(t, getSource(t, WorkloadKindDeployment, "api").Spec.DisableInstrumentation)
}

func TestSyncWorkloadsInNamespace_DeselectingDisabledSourceWhileNamespaceTurnsOn(t *testing.T) {
	recorder := setupFakePersistClient(t, existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", true, map[string]string{testStream: "true"}))

	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{
		persistWorkload("cart", false),
		persistNamespace(true),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{workloadID(k8sconsts.WorkloadKindNamespace, testNs)}, recorder.creates, "the namespace source is still written")
	assert.True(t, getSource(t, WorkloadKindDeployment, "cart").Spec.DisableInstrumentation)
}

func TestToggleSourceCRD_DeselectingOtherStreamSourceKeepsIt(t *testing.T) {
	const otherStream = "payments"
	recorder := setupFakePersistClient(t, existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", false, map[string]string{otherStream: "true"}))

	err := ToggleSourceCRD(context.Background(), testNs, "cart", WorkloadKindDeployment, false, testStream)
	require.NoError(t, err)

	assert.Empty(t, recorder.snapshots)
	assert.Equal(t, "true", streamLabel(getSource(t, WorkloadKindDeployment, "cart"), otherStream))
}

func TestSyncWorkloadsInNamespace_ReEnablingDisabledSourceJoinsOnlyCurrentStream(t *testing.T) {
	const currentStream = "payments"
	recorder := setupFakePersistClient(t, existingSource("cart-source", k8sconsts.WorkloadKindDeployment, "cart", true, map[string]string{testStream: "true"}))

	workload := persistWorkload("cart", true)
	workload.CurrentStreamName = currentStream
	err := SyncWorkloadsInNamespace(context.Background(), []*model.PersistNamespaceSourceInput{workload})
	require.NoError(t, err)

	require.Len(t, recorder.snapshots, 1, "the flag and labels change in one patch")
	source := getSource(t, WorkloadKindDeployment, "cart")
	assert.False(t, source.Spec.DisableInstrumentation)
	assert.Equal(t, "true", streamLabel(source, currentStream))
	_, hasDefault := source.Labels[k8sconsts.SourceDataStreamLabelPrefix+testStream]
	assert.False(t, hasDefault, "the stream from before it was disabled is not rejoined")
}
