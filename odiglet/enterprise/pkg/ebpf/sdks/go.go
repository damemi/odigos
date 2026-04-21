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

	"github.com/odigos-io/ebpf-core/pkg/wasp"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/instrumentationrules"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/consts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"
	odigosebpf "github.com/odigos-io/odigos/odiglet/pkg/ebpf"
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

var offsetsFilePathEnv = "OTEL_GO_OFFSETS_FILE"

type GoOtelEbpfSdkEnterprise struct {
	inst             *auto.Instrumentation
	cp               *odigosebpf.ConfigProvider[goAutoConfig.InstrumentationConfig]
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
}

func NewGoInstrumentationFactoryEnterprise(traceIdSuffix uint8, ws wasp.MapsProvider) (instrumentation.Factory, error) {
	g := &GoInstrumentationFactoryEnterprise{
		offsetLastModified: time.Time{},
		waspMapsProvider:   ws,
		traceIdSuffix:      traceIdSuffix,
		logger:             commonlogger.LoggerCompat().With("subsystem", "ebpfgosdk"),
	}
	err := g.updateLocalOffsets()
	if err != nil {
		return nil, err
	}

	return g, nil
}

func (g *GoInstrumentationFactoryEnterprise) updateLocalOffsets() error {
	// if the offsets file var is set, check if it has been modified
	if path := os.Getenv(offsetsFilePathEnv); path != "" {
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
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("localhost:%d", consts.OTLPPort)),
	)
	if err != nil {
		g.logger.Error("failed to create exporter", "error", err)
		return nil, err
	}

	// Fetch initial config based on the InstrumentationConfig CR
	initialConfig, err := convertToGoInstrumentationConfig(settings.InitialConfig)
	if err != nil {
		return nil, fmt.Errorf("invalid initial config type, expected *odigosv1.SdkConfig, got %T", settings.InitialConfig)
	}

	cp := odigosebpf.NewConfigProvider(initialConfig)

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

func (g *GoOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, sdkConfig instrumentation.Config) error {
	updatedConfig, convertErr := convertToGoInstrumentationConfig(sdkConfig)
	if convertErr != nil {
		return convertErr
	}

	return g.cp.SendConfig(ctx, updatedConfig)
}

func mergeBooleanConfigValues(defaultValue *bool, libValue *bool) bool {
	// if the lib is configuring this value, we take it
	if libValue != nil {
		return *libValue
	}

	// if the lib is not configuring this value, we take the default value
	if defaultValue != nil {
		return *defaultValue
	}

	// if neither the lib nor the default value is set, we return false (disabled)
	return false
}

// returns the boolean value, but fallbacks to "false" if it is nil (from *bool to bool)
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

// Deprecated: this is a duplication of the OSS odiglet code, used to avoid breaking the enterprise odiglet
// Once the enterprise odiglet is updated to use the generic config provider, this function will be removed
func convertToGoInstrumentationConfig(sdkConfig instrumentation.Config) (goAutoConfig.InstrumentationConfig, error) {
	config, ok := sdkConfig.(*odigosv1.SdkConfig)
	if !ok {
		return goAutoConfig.InstrumentationConfig{}, fmt.Errorf("invalid initial config type, expected *odigosv1.SdkConfig, got %T", sdkConfig)
	}
	ic := goAutoConfig.InstrumentationConfig{}
	if sdkConfig == nil {
		return ic, nil
	}
	ic.InstrumentationLibraryConfigs = make(map[goAutoConfig.InstrumentationLibraryID]goAutoConfig.InstrumentationLibrary)
	for _, ilc := range config.InstrumentationLibraryConfigs {
		libID := goAutoConfig.InstrumentationLibraryID{
			InstrumentedPkg: ilc.InstrumentationLibraryId.InstrumentationLibraryName,
			SpanKind:        common.SpanKindOdigosToOtel(ilc.InstrumentationLibraryId.SpanKind),
		}
		var tracesEnabled *bool
		if ilc.TraceConfig != nil {
			tracesEnabled = ilc.TraceConfig.Enabled
		}
		// go config must get boolean (true or false), while odigos semantics allows also nil, which means "don't care" (use default or false).
		// we resolve these here, to convert the odigos schema to the go instrumentation schema.
		var codeAttributes *goAutoConfig.CodeAttributes
		if ilc.CodeAttributes != nil {
			if config.DefaultCodeAttributes != nil {
				// if there is a global default, merge it with the library specific config
				codeAttributes = &goAutoConfig.CodeAttributes{
					Column:     mergeBooleanConfigValues(config.DefaultCodeAttributes.Column, ilc.CodeAttributes.Column),
					FilePath:   mergeBooleanConfigValues(config.DefaultCodeAttributes.FilePath, ilc.CodeAttributes.FilePath),
					Function:   mergeBooleanConfigValues(config.DefaultCodeAttributes.Function, ilc.CodeAttributes.Function),
					LineNumber: mergeBooleanConfigValues(config.DefaultCodeAttributes.LineNumber, ilc.CodeAttributes.LineNumber),
					Namespace:  mergeBooleanConfigValues(config.DefaultCodeAttributes.Namespace, ilc.CodeAttributes.Namespace),
					Stacktrace: mergeBooleanConfigValues(config.DefaultCodeAttributes.Stacktrace, ilc.CodeAttributes.Stacktrace),
				}
			} else {
				// if there is library config but not global config, use the library config as is
				ilcCodeAttributes := convertCodeAttributesToInstrumentationConfig(ilc.CodeAttributes)
				codeAttributes = &ilcCodeAttributes
			}
		}

		var payloadCollection goAutoConfig.PayloadConfig
		if ilc.PayloadCollection != nil {
			payloadCollection = convertPayloadCollection(ilc.PayloadCollection)
		}

		ic.InstrumentationLibraryConfigs[libID] = goAutoConfig.InstrumentationLibrary{
			TracesEnabled:  tracesEnabled,
			CodeAttributes: codeAttributes,
			Payload:        &payloadCollection,
		}
	}

	// defaults
	ic.DefaultCodeAttributes = convertCodeAttributesToInstrumentationConfig(config.DefaultCodeAttributes)
	if config.DefaultTraceConfig != nil && config.DefaultTraceConfig.Disabled != nil {
		ic.DefaultTracesDisabled = *config.DefaultTraceConfig.Disabled
	}

	if config.CustomInstrumentations != nil {
		for _, probe := range config.CustomInstrumentations.Golang {
			ic.CustomInstrumentations = append(ic.CustomInstrumentations, goAutoConfig.CustomInstrumentation{
				PackageName:        probe.PackageName,
				FunctionName:       probe.FunctionName,
				ReceiverName:       probe.ReceiverName,
				ReceiverMethodName: probe.ReceiverMethodName,
			})
		}
	}

	if config.DefaultHeadersCollection != nil {
		// note: currently the CRD has no notion of response vs request headers
		ic.HTTPHeaders.RequestKeys = config.DefaultHeadersCollection.HeaderKeys
		ic.HTTPHeaders.ResponseKeys = config.DefaultHeadersCollection.HeaderKeys
	}

	if payloadConfig := config.DefaultPayloadCollection; payloadConfig != nil {
		ic.Payload = convertPayloadCollection(payloadConfig)
	}

	// TODO: take sampling config from the CR
	// sampling is not yet supported in the enterprise go instrumentation
	//  ic.Sampler = auto.DefaultSampler()
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
