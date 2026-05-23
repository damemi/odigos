package sdks

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/auto"
	goAutoConfig "go.opentelemetry.io/auto/config"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc"

	"github.com/odigos-io/ebpf-core/pkg/wasp"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/logcapture"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	commonapisampling "github.com/odigos-io/odigos/common/api/sampling"
	"github.com/odigos-io/odigos/common/configprovider"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
)

const rsaPublicKeyString = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAmMXVUgppNeHZA507GFX3
Pg2c+p95AFOJ1SY1OYklZoHYu3wEsx0mdsr2shSXMMvsbekMTlyeJ4L5SUgGLDyh
h57aBYC4WP9SGE1btJt3Faz/mZRsGgsV/JoqnezHzEDZBaNtmiszf0kzLNcd2RxE
JnMVMEH3i2h0lQhKNwGcLimijopdrWgnMMcnVXB8DUrQrVqK+OjxIN/Uv23jv4Vz
vjS5OxdhOOAlRNp+44JZg5ISnRt8VnM+ETDE6UOF5WcKES+r7WFFxT1q+XPnIYix
eNabH8dpHROXDm8+N9JI/yMWUChnQe9oqSiCNj/xc5VYaFrdLCbpsBGRT7lVL9fC
bwIDAQAB
-----END PUBLIC KEY-----`

var GoOffsetsFilePathEnv = "OTEL_GO_OFFSETS_FILE"

// goOffsetsWatchInterval is how often WatchGoOffsets polls the offsets file for changes.
// Polling is used (rather than fsnotify) because the file is typically mounted from a Kubernetes
// ConfigMap, which updates the file via an atomic rename of the parent directory; the resulting
// fsnotify events are fragile across implementations whereas an os.Stat call is reliable.
const goOffsetsWatchInterval = 30 * time.Second

// GoEnterpriseDistroName is the OTel distribution name registered for the enterprise Go eBPF SDK.
const GoEnterpriseDistroName = "golang-enterprise"

type GoOtelEbpfSdkEnterprise struct {
	inst             *auto.Instrumentation
	cp               *configprovider.ConfigProvider[goAutoConfig.InstrumentationConfig]
	waspMaps         *wasp.Maps
	waspMapsProvider wasp.MapsProvider
	podName          string
}

type GoInstrumentationFactoryEnterprise struct {
	offsetLastModified time.Time
	offsetFileBytes    []byte
	waspMapsProvider   wasp.MapsProvider
	traceIdSuffix      uint8
	logger             *commonlogger.OdigosLogger
	otlpConn           *grpc.ClientConn
}

func NewGoInstrumentationFactoryEnterprise(traceIdSuffix uint8, ws wasp.MapsProvider, otlpConn *grpc.ClientConn) (instrumentation.Factory, error) {
	if otlpConn == nil {
		return nil, errNoOTLPConn
	}
	g := &GoInstrumentationFactoryEnterprise{
		offsetLastModified: time.Time{},
		waspMapsProvider:   ws,
		traceIdSuffix:      traceIdSuffix,
		logger:             commonlogger.LoggerCompat().With("subsystem", "ebpfgosdk"),
		otlpConn:           otlpConn,
	}
	if err := g.updateLocalOffsets(); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *GoInstrumentationFactoryEnterprise) updateLocalOffsets() error {
	// if the offsets file var is set, check if it has been modified
	if path := os.Getenv(GoOffsetsFilePathEnv); path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}

		// If the file is empty, use default offsets
		if info.Size() == 0 {
			g.offsetFileBytes = []byte{}
			g.logger.Info("no custom Go offsets loaded, falling back to default offsets")
			return nil
		}

		// if the modtime has been updated, read the new bytes
		if info.ModTime().After(g.offsetLastModified) {
			file, err := readAndVerifyOffsetsFile(path)
			if err != nil {
				return err
			}

			// Only decode the fields we need for logging; avoid map[string]interface{}
			// which allocates for the entire offsets document.
			var meta struct {
				Timestamp json.RawMessage `json:"timestamp"`
			}
			if err := json.Unmarshal(file, &meta); err != nil {
				return err
			}

			g.logger.Info("using custom Go offsets file", "timestamp", string(meta.Timestamp))

			g.offsetFileBytes = file
			g.offsetLastModified = info.ModTime()
		}
	} else {
		// if the env var isn't set, clear the local offset file
		// this allows switching back to in-memory offsets by deleting the env var
		g.offsetFileBytes = []byte{}
		g.logger.Info("custom Go offsets file env var not set, falling back to default offsets")
	}

	return nil
}

// WatchGoOffsets polls the custom Go offsets file (OTEL_GO_OFFSETS_FILE) for changes and, on each
// detected update, asks the OSS instrumentation manager to retry failed Go instrumentations. It
// blocks until ctx is canceled. If OTEL_GO_OFFSETS_FILE is not set, it returns immediately.
func WatchGoOffsets(ctx context.Context, requestsChannel ebpf.K8sInstrumentationRequests, logger *commonlogger.OdigosLogger) error {
	path := os.Getenv(GoOffsetsFilePathEnv)
	if path == "" {
		logger.Info("custom Go offsets file env var not set, not watching for changes")
		return nil
	}

	var lastSeen time.Time
	if info, err := os.Stat(path); err == nil {
		lastSeen = info.ModTime()
	}

	ticker := time.NewTicker(goOffsetsWatchInterval)
	defer ticker.Stop()

	logger.Info("watching custom Go offsets file for changes", "path", path, "interval", goOffsetsWatchInterval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil {
				if !os.IsNotExist(err) {
					logger.Debug("failed to stat custom Go offsets file", "path", path, "error", err)
				}
				continue
			}
			if !info.ModTime().After(lastSeen) {
				continue
			}
			modTime := info.ModTime()

			req := instrumentation.Request[ebpf.K8sProcessGroup, ebpf.K8sConfigGroup, *ebpf.K8sProcessDetails]{
				RetryDistros: []string{GoEnterpriseDistroName},
			}
			select {
			case requestsChannel <- req:
				lastSeen = modTime
				logger.Info("detected custom Go offsets file change, requesting retry of failed Go instrumentations", "modtime", modTime)
			case <-ctx.Done():
				return nil
			default:
				logger.Info("instrumentation requests channel full, dropping Go offsets retry signal", "modtime", modTime)
			}
		}
	}
}

func podNameFromSettings(settings instrumentation.Settings) string {
	// getting the pod name from resource attributes is not the most elegant way of doing this
	// consider the instrumentation.Settings is declared in OSS, and we don't want to put too much details there
	for _, kv := range settings.ResourceAttributes {
		if kv.Key == semconv.K8SPodNameKey {
			return kv.Value.AsString()
		}
	}

	return ""
}

func (g *GoInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	err := g.updateLocalOffsets()
	if err != nil {
		g.logger.Error("failed to update custom Go offsets, attempting to continue with default offsets", "error", err)
		g.offsetFileBytes = []byte{}
	}

	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithGRPCConn(g.otlpConn),
	)
	if err != nil {
		g.logger.Error("failed to create exporter", "error", err)
		return nil, err
	}

	// Fetch initial config based on the InstrumentationConfig CR
	initialConfig, err := convertToGoInstrumentationConfig(settings.InitialConfig)
	if err != nil {
		if shutdownErr := defaultExporter.Shutdown(ctx); shutdownErr != nil {
			g.logger.Error("failed to shutdown exporter after config error", "error", shutdownErr)
		}
		return nil, fmt.Errorf("invalid initial config type, expected *odigosv1.ContainerAgentConfig, got %T", settings.InitialConfig)
	}

	cp := configprovider.NewConfigProvider(initialConfig)

	// TODO: once we have all the desired functionality in the generic config provider this code should
	// be refactored to call the OSS Odiglet factory
	opts := []auto.InstrumentationOption{
		auto.WithEnv(), // for OTEL_LOG_LEVEL
		auto.WithPID(pid),
		auto.WithResourceAttributes(settings.ResourceAttributes...),
		auto.WithServiceName(settings.ServiceName),
		auto.WithTraceExporter(defaultExporter),
		auto.WithGlobal(),
		auto.WithConfigProvider(cp),
	}

	if len(g.offsetFileBytes) > 0 {
		opts = append(opts, auto.WithOffsetFile(g.offsetFileBytes))
	}

	var (
		waspMaps *wasp.Maps
		podName  string
	)

	if g.waspMapsProvider != nil {
		if podName = podNameFromSettings(settings); podName != "" {
			g.logger.Info("using wasp handshake for Go process", "pod", podName)
			waspMaps, err = g.waspMapsProvider.Release(podName, pid)
			if err != nil {
				g.logger.Error("failed to acquire wasp maps", "error", err)
			} else {
				opts = append(opts, auto.WithWASPHandshakeMaps(waspMaps))
			}
		}
	}
	if g.traceIdSuffix != 0 {
		opts = append(opts, auto.WithTraceIdSuffix(g.traceIdSuffix))
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		opts...,
	)
	if err != nil {
		g.logger.Error("instrumentation setup failed", "error", err)
		if shutdownErr := defaultExporter.Shutdown(ctx); shutdownErr != nil {
			g.logger.Error("failed to shutdown exporter after instrumentation setup failure", "error", shutdownErr)
		}
		if waspMaps != nil {
			// return ownership of the wasp maps to the provider in case we failed to instrument this process
			err = errors.Join(err, g.waspMapsProvider.Take(podName, waspMaps))
		}
		return nil, err
	}

	goInstWrapper := &GoOtelEbpfSdkEnterprise{
		inst:             inst,
		cp:               cp,
		waspMaps:         waspMaps,
		waspMapsProvider: g.waspMapsProvider,
		podName:          podName,
	}

	return goInstWrapper, nil
}

func (g *GoOtelEbpfSdkEnterprise) Load(ctx context.Context) (instrumentation.Status, error) {
	probeLoadStatus, err := g.inst.Load(ctx)
	if err != nil {
		if g.waspMaps != nil {
			// if we failed to load, and the instrumentation took ownership over the wasp maps
			// we return ownership back to the provider
			err = errors.Join(err, g.waspMapsProvider.Take(g.podName, g.waspMaps))
		}
		return instrumentation.Status{}, err
	}

	status := instrumentation.Status{
		Components: make(map[string]error),
	}
	for probeID, err := range probeLoadStatus {
		status.Components[fmt.Sprintf("%s/%s", probeID.InstrumentedPkg, probeID.SpanKind.String())] = err
	}

	return status, nil
}

// PrepareLogCapture attaches the Go goroutine-id map and runtime offsets to the
// log capture manager so logs emitted from this process can be correlated with
// their originating span. Invoked by logCaptureDecorator immediately before
// RegisterProcess.
func (g *GoOtelEbpfSdkEnterprise) PrepareLogCapture(ctx context.Context, pid int) {
	scMap := g.inst.GetGoroutineIdToScMap()
	if scMap == nil {
		return
	}
	goidPos, ancestorsPos, err := g.inst.GetGoRuntimeOffsets()
	if err != nil {
		commonlogger.LoggerCompat().With("subsystem", "ebpfgosdk").Info("failed to get Go runtime offsets for log correlation", "pid", pid, "error", err)
		return
	}
	logcapture.GetInstance().AddGoroutineMap(uint32(pid), scMap, goidPos, ancestorsPos)
}

func (g *GoOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *GoOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	var err error
	if g.waspMaps != nil {
		// if the instrumentation had ownership of wasp maps - return ownership to the provider
		// this will allow us to re-use the wasp maps if a new instrumentation is required in the same pod after this.
		err = errors.Join(err, g.waspMapsProvider.Take(g.podName, g.waspMaps))
	}
	err = errors.Join(err, g.inst.Close())
	return err
}

func (g *GoOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, containerConfig instrumentation.Config) error {
	updatedConfig, convertErr := convertToGoInstrumentationConfig(containerConfig)
	if convertErr != nil {
		return convertErr
	}
	return g.cp.SendConfig(ctx, updatedConfig)
}

func booleanValueWithDefault(value *bool) bool {
	if value == nil {
		return false
	}
	return *value
}

func convertCodeAttributesToInstrumentationConfig(rule *instrumentationrules.CodeAttributes) goAutoConfig.CodeAttributes {
	if rule == nil {
		return goAutoConfig.CodeAttributes{}
	} else {
		return goAutoConfig.CodeAttributes{
			Column:     booleanValueWithDefault(rule.Column),
			FilePath:   booleanValueWithDefault(rule.FilePath),
			Function:   booleanValueWithDefault(rule.Function),
			LineNumber: booleanValueWithDefault(rule.LineNumber),
			Namespace:  booleanValueWithDefault(rule.Namespace),
			Stacktrace: booleanValueWithDefault(rule.Stacktrace),
		}
	}
}

func convertToGoInstrumentationConfig(config instrumentation.Config) (goAutoConfig.InstrumentationConfig, error) {
	containerConfig, ok := config.(*odigosv1.ContainerAgentConfig)
	if !ok {
		return goAutoConfig.InstrumentationConfig{}, fmt.Errorf("invalid config type, expected *odigosv1.ContainerAgentConfig, got %T", config)
	}
	ic := goAutoConfig.InstrumentationConfig{}
	if containerConfig == nil || containerConfig.Traces == nil {
		return ic, nil
	}

	traces := containerConfig.Traces

	if traces.TraceVerbosity != nil {
		ic.InstrumentationLibraryConfigs = make(map[goAutoConfig.InstrumentationLibraryID]goAutoConfig.InstrumentationLibrary)
		falseVal := false
		trueVal := true
		for _, lib := range traces.TraceVerbosity.DisabledLibraries {
			libID := goAutoConfig.InstrumentationLibraryID{InstrumentedPkg: lib.LibraryName}
			ic.InstrumentationLibraryConfigs[libID] = goAutoConfig.InstrumentationLibrary{TracesEnabled: &falseVal}
		}
		for _, lib := range traces.TraceVerbosity.EnabledLibraries {
			libID := goAutoConfig.InstrumentationLibraryID{InstrumentedPkg: lib.LibraryName}
			ic.InstrumentationLibraryConfigs[libID] = goAutoConfig.InstrumentationLibrary{TracesEnabled: &trueVal}
		}
	}

	ic.DefaultCodeAttributes = convertCodeAttributesToInstrumentationConfig(traces.CodeAttributes)

	if traces.CustomInstrumentations != nil {
		for _, probe := range traces.CustomInstrumentations.Golang {
			ic.CustomInstrumentations = append(ic.CustomInstrumentations, goAutoConfig.CustomInstrumentation{
				PackageName:        probe.PackageName,
				FunctionName:       probe.FunctionName,
				ReceiverName:       probe.ReceiverName,
				ReceiverMethodName: probe.ReceiverMethodName,
			})
		}
	}

	if traces.HeadersCollection != nil {
		// note: currently the CRD has no notion of response vs request headers
		ic.HTTPHeaders.RequestKeys = traces.HeadersCollection.HeaderKeys
		ic.HTTPHeaders.ResponseKeys = traces.HeadersCollection.HeaderKeys
	}

	if traces.PayloadCollection != nil {
		ic.Payload = convertPayloadCollection(traces.PayloadCollection)
	}

	if traces.HeadSampling != nil {
		ic.HeadSampling = convertHeadSampling(traces.HeadSampling)
	}

	return ic, nil
}

func convertPayloadCollection(payloadConfig *instrumentationrules.PayloadCollection) goAutoConfig.PayloadConfig {
	goPayloadConfig := goAutoConfig.PayloadConfig{}
	if payloadConfig != nil {
		if payloadConfig.HttpRequest != nil {
			goPayloadConfig.HTTPRequest = &goAutoConfig.HTTPPayloadCollectionSettings{Enabled: true}
		}
		if payloadConfig.HttpResponse != nil {
			goPayloadConfig.HTTPResponse = &goAutoConfig.HTTPPayloadCollectionSettings{Enabled: true}
		}
		if payloadConfig.DbQuery != nil {
			goPayloadConfig.DbQuery = &goAutoConfig.DbQuerySettings{Enabled: true}
		}
		if payloadConfig.Messaging != nil {
			goPayloadConfig.Messaging = &goAutoConfig.MessagingSettings{Enabled: true}
		}
	}
	return goPayloadConfig
}

func convertHeadSampling(hs *commonapisampling.HeadSamplingConfig) *goAutoConfig.HeadSamplingConfig {
	cfg := &goAutoConfig.HeadSamplingConfig{
		DryRun:          hs.DryRun,
		SpanMetricsMode: goAutoConfig.SpanMetricsMode(hs.SpanMetricsMode),
	}
	for _, no := range hs.NoisyOperations {
		cfg.NoisyOperations = append(cfg.NoisyOperations, convertNoisyOperation(no))
	}
	return cfg
}

func convertNoisyOperation(no commonapisampling.NoisyOperation) goAutoConfig.NoisyOperationRule {
	rule := goAutoConfig.NoisyOperationRule{
		ID:               no.Id,
		Name:             no.Name,
		Disabled:         no.Disabled,
		PercentageAtMost: no.PercentageAtMost,
	}
	if no.Operation != nil {
		rule.Operation = &goAutoConfig.HeadSamplingOperationMatcher{}
		if no.Operation.HttpServer != nil {
			rule.Operation.HTTPServer = &goAutoConfig.HeadSamplingHTTPServerMatcher{
				Route:       no.Operation.HttpServer.Route,
				RoutePrefix: no.Operation.HttpServer.RoutePrefix,
				Method:      no.Operation.HttpServer.Method,
			}
		}
		if no.Operation.HttpClient != nil {
			rule.Operation.HTTPClient = &goAutoConfig.HeadSamplingHTTPClientMatcher{
				ServerAddress:       no.Operation.HttpClient.ServerAddress,
				TemplatedPath:       no.Operation.HttpClient.TemplatedPath,
				TemplatedPathPrefix: no.Operation.HttpClient.TemplatedPathPrefix,
				Method:              no.Operation.HttpClient.Method,
			}
		}
	}
	return rule
}

func readAndVerifyOffsetsFile(path string) ([]byte, error) {
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var decoded string
	err = json.Unmarshal(fileBytes, &decoded)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(decoded, "---SIGNATURE---")
	if len(parts) != 2 {
		return nil, errors.New("Invalid file format. Signature delimiter not found.")
	}

	originalContent := parts[0]
	signatureBase64 := strings.TrimSpace(parts[1])

	// Decode the Base64-encoded signature
	signature, err := base64.StdEncoding.DecodeString(signatureBase64)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode([]byte(rsaPublicKeyString))
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("Failed to decode PEM block containing public key")
	}

	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	rsaPublicKey, ok := publicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("Public key is not of type RSA")
	}

	h := sha256.New()
	if _, err := io.WriteString(h, originalContent); err != nil {
		return nil, err
	}

	err = rsa.VerifyPKCS1v15(rsaPublicKey, crypto.SHA256, h.Sum(nil), signature)
	if err != nil {
		return nil, err
	}

	return []byte(originalContent), nil
}
