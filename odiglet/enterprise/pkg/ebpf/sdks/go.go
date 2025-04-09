package sdks

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"encoding/base64"
	"encoding/pem"

	"go.opentelemetry.io/auto"
	goAutoConfig "go.opentelemetry.io/auto/config"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/trace"
	apitrace "go.opentelemetry.io/otel/trace"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/api/odigos/v1alpha1/instrumentationrules"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/k8sutils/pkg/service"
	odigosebpf "github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
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
	inst *auto.Instrumentation
	cp   *odigosebpf.ConfigProvider[goAutoConfig.InstrumentationConfig]
}

type GoInstrumentationFactoryEnterprise struct {
	offsetLastModified time.Time
	offsetFile         io.Reader
}

func NewGoInstrumentationFactoryEnterprise() (instrumentation.Factory, error) {
	g := &GoInstrumentationFactoryEnterprise{
		offsetLastModified: time.Time{},
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

		// if the modtime has been updated, read the new bytes
		if info.ModTime().After(g.offsetLastModified) {
			file, err := readAndVerifyOffsetsFile(path)
			if err != nil {
				return err
			}
			g.offsetFile = file
			g.offsetLastModified = info.ModTime()
		}
	} else {
		// if the env var isn't set, clear the local offset file
		// this allows switching back to in-memory offsets by deleting the env var
		g.offsetFile = strings.NewReader("")
	}

	return nil
}

func (g *GoInstrumentationFactoryEnterprise) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	err := g.updateLocalOffsets()
	if err != nil {
		return nil, err
	}

	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(service.LocalTrafficOTLPGrpcDataCollectionEndpoint(env.Current.NodeIP)),
	)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
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

	if g.offsetFile != nil {
		opts = append(opts, auto.WithOffsetFile(g.offsetFile))
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		opts...,
	)
	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	goInstWrapper := &GoOtelEbpfSdkEnterprise{
		inst: inst,
		cp:   cp,
	}

	// payload collection configuration is done separately from the general config because of the deprecated ConfigBool method
	// that is used to set the payload collection configuration.
	// Once we replace the ConfigBool method with the generic config provider, we can remove this and pass the
	// payload collection as part of the initial configuration to the config provider.
	err = goInstWrapper.configPayloadCollection(settings.InitialConfig)
	if err != nil {
		log.Logger.Error(err, "failed to apply payload collection config based on initial config")
	}

	return goInstWrapper, nil
}

func (g *GoOtelEbpfSdkEnterprise) Load(ctx context.Context) error {
	return g.inst.Load(ctx)
}

func (g *GoOtelEbpfSdkEnterprise) Run(ctx context.Context) error {
	return g.inst.Run(ctx)
}

func (g *GoOtelEbpfSdkEnterprise) Close(ctx context.Context) error {
	return g.inst.Close()
}

// Deprecated: this function will be removed in the future once we'll move to the generic config provider
func (g *GoOtelEbpfSdkEnterprise) configBool(instrumentedModuleName string, optionKey string, spanKind apitrace.SpanKind, optionValue bool) error {

	// temporary workaround to avoid setting a config option that is not supported with this instance
	supportedConfigOptions := g.inst.GetConfigOptions()
	if _, ok := supportedConfigOptions[instrumentedModuleName]; !ok {
		// noop, in case the instrumentation library has not been loaded
		return nil
	}
	for _, option := range supportedConfigOptions[instrumentedModuleName] {
		if option.OptionKey == optionKey && option.SpanKind == spanKind {
			return g.inst.ConfigBool(instrumentedModuleName, optionKey, spanKind, optionValue)
		}
	}
	// if we get here, it means none of the options matched thus this config is not supported
	// make it a silent noop
	return nil
}

func (g *GoOtelEbpfSdkEnterprise) ApplyConfig(ctx context.Context, sdkConfig instrumentation.Config) error {
	// We do the payload collection configuration separately from the general config because of the deprecated ConfigBool method
	// that is used to set the payload collection configuration.
	// Once we replace the ConfigBool method with the generic config provider, we can merge the two methods.
	payloadCollectionErr := g.configPayloadCollection(sdkConfig)

	updatedConfig, convertErr := convertToGoInstrumentationConfig(sdkConfig)
	if convertErr != nil {
		return errors.Join(payloadCollectionErr, convertErr)
	}

	return g.cp.SendConfig(ctx, updatedConfig)
}

func (g *GoOtelEbpfSdkEnterprise) configPayloadCollection(config instrumentation.Config) error {
	sdkConfig, ok := config.(*odigosv1.SdkConfig)
	if !ok {
		return fmt.Errorf("invalid config type, expected *odigosv1.SdkConfig, got %T", sdkConfig)
	}
	// specific instructions for the go libraries:

	// net/http server
	payloadCollectionNetHttpServer := getPayloadCollectionConfig(sdkConfig, odigosv1.InstrumentationLibraryId{
		InstrumentationLibraryName: "net/http",
		SpanKind:                   common.ServerSpanKind,
	})
	collectResponseNetHttpServer := payloadCollectionNetHttpServer.HttpResponse != nil
	log.Logger.V(0).Info("Applying config to golang net/http server", "collectResponse", collectResponseNetHttpServer)
	err := g.configBool("net/http", "http.response.body", trace.SpanKindServer, collectResponseNetHttpServer)
	if err != nil {
		return err
	}
	err = g.configBool("net/http", "http.response.header.content-length", trace.SpanKindServer, collectResponseNetHttpServer)
	if err != nil {
		return err
	}

	// net/http client
	payloadCollectionNetHttpClient := getPayloadCollectionConfig(sdkConfig, odigosv1.InstrumentationLibraryId{
		InstrumentationLibraryName: "net/http",
		SpanKind:                   common.ClientSpanKind,
	})
	collectRequestNetHttpClient := payloadCollectionNetHttpClient.HttpRequest != nil
	collectResponseNetHttpClient := payloadCollectionNetHttpClient.HttpResponse != nil

	log.Logger.V(0).Info("Applying config to golang net/http client", "collectResponse", collectResponseNetHttpClient, "collectRequest", collectRequestNetHttpClient)
	err = g.configBool("net/http", "http.request.body", trace.SpanKindClient, collectRequestNetHttpClient)
	if err != nil {
		return err
	}
	err = g.configBool("net/http", "http.response.header.content-length", trace.SpanKindClient, collectResponseNetHttpClient)
	if err != nil {
		return err
	}

	// database/sql
	payloadCollectionNetDatabaseSql := getPayloadCollectionConfig(sdkConfig, odigosv1.InstrumentationLibraryId{
		InstrumentationLibraryName: "database/sql",
		SpanKind:                   common.ClientSpanKind,
	})
	collectDbQueryDatabaseSql := payloadCollectionNetDatabaseSql.DbQuery != nil
	log.Logger.V(0).Info("Applying config to golang database/sql", "collectDbQuery", collectDbQueryDatabaseSql)
	err = g.configBool("database/sql", "db.query.text", trace.SpanKindClient, collectDbQueryDatabaseSql)
	if err != nil {
		return err
	}

	// github.com/ClickHouse/clickhouse-go/v2
	payloadCollectionClickhouse := getPayloadCollectionConfig(sdkConfig, odigosv1.InstrumentationLibraryId{
		InstrumentationLibraryName: "github.com/ClickHouse/clickhouse-go/v2",
		SpanKind:                   common.ClientSpanKind,
	})
	collectDbQueryClickhouse := payloadCollectionClickhouse.DbQuery != nil
	log.Logger.V(0).Info("Applying config to golang github.com/ClickHouse/clickhouse-go/v2", "collectDbQuery", collectDbQueryClickhouse)
	err = g.configBool("github.com/ClickHouse/clickhouse-go/v2", "db.query.text", trace.SpanKindClient, collectDbQueryClickhouse)
	if err != nil {
		return err
	}

	// casandra
	payloadCollectionCassandra := getPayloadCollectionConfig(sdkConfig, odigosv1.InstrumentationLibraryId{
		InstrumentationLibraryName: "github.com/gocql/gocql",
		SpanKind:                   common.ClientSpanKind,
	})
	collectDbQueryCassandra := payloadCollectionCassandra.DbQuery != nil
	log.Logger.V(0).Info("Applying config to golang github.com/gocql/gocql", "collectDbQuery", collectDbQueryCassandra)
	err = g.configBool("github.com/gocql/gocql", "db.query.text", trace.SpanKindClient, collectDbQueryCassandra)
	if err != nil {
		return err
	}

	// kafka producer
	payloadCollectionKafka := getPayloadCollectionConfig(sdkConfig, odigosv1.InstrumentationLibraryId{
		InstrumentationLibraryName: "github.com/segmentio/kafka-go",
		SpanKind:                   common.ProducerSpanKind,
	})
	collectKafkaMessageKafka := payloadCollectionKafka.Messaging != nil
	log.Logger.V(0).Info("Applying config to golang github.com/segmentio/kafka-go producer", "collectKafkaMessage", collectKafkaMessageKafka)
	err = g.configBool("github.com/segmentio/kafka-go", "messaging.message.body", trace.SpanKindProducer, collectKafkaMessageKafka)
	if err != nil {
		return err
	}

	// kafka consumer
	payloadCollectionKafkaConsumer := getPayloadCollectionConfig(sdkConfig, odigosv1.InstrumentationLibraryId{
		InstrumentationLibraryName: "github.com/segmentio/kafka-go",
		SpanKind:                   common.ConsumerSpanKind,
	})
	collectKafkaMessageKafkaConsumer := payloadCollectionKafkaConsumer.Messaging != nil
	log.Logger.V(0).Info("Applying config to golang github.com/segmentio/kafka-go consumer", "collectKafkaMessage", collectKafkaMessageKafkaConsumer)
	err = g.configBool("github.com/segmentio/kafka-go", "messaging.message.body", trace.SpanKindConsumer, collectKafkaMessageKafkaConsumer)
	return err
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
		log.Logger.V(0).Info("No SDK config provided for Go instrumentation, using default")
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

		ic.InstrumentationLibraryConfigs[libID] = goAutoConfig.InstrumentationLibrary{
			TracesEnabled:  tracesEnabled,
			CodeAttributes: codeAttributes,
		}
	}
	ic.DefaultCodeAttributes = convertCodeAttributesToInstrumentationConfig(config.DefaultCodeAttributes)

	// TODO: take sampling config from the CR
	// sampling is not yet supported in the enterprise go instrumentation
	//  ic.Sampler = auto.DefaultSampler()
	return ic, nil
}

func getPayloadCollectionConfig(spec *odigosv1.SdkConfig, instrumentationLibraryId odigosv1.InstrumentationLibraryId) instrumentationrules.PayloadCollection {

	// for each library, we use the content from the libraries config if there is any,
	// otherwise we take the default config from the SDK config.

	instrumentationLibraryConfig := findInstrumentationLibraryConfig(spec, instrumentationLibraryId)
	var instLibPayloadCollection *instrumentationrules.PayloadCollection
	if instrumentationLibraryConfig != nil {
		instLibPayloadCollection = instrumentationLibraryConfig.PayloadCollection
	}
	return mergePayloadConfigs(spec.DefaultPayloadCollection, instLibPayloadCollection)
}

func findInstrumentationLibraryConfig(spec *odigosv1.SdkConfig, instrumentationLibraryId odigosv1.InstrumentationLibraryId) *odigosv1.InstrumentationLibraryConfig {
	for _, libConfig := range spec.InstrumentationLibraryConfigs {
		if libConfig.InstrumentationLibraryId == instrumentationLibraryId {
			return &libConfig
		}
	}
	return nil
}

func mergePayloadConfigs(defaultConfig *instrumentationrules.PayloadCollection, libConfig *instrumentationrules.PayloadCollection) instrumentationrules.PayloadCollection {
	mergedConfig := instrumentationrules.PayloadCollection{}
	if defaultConfig != nil {
		mergedConfig = *defaultConfig
	}

	// if there is a libConfig, we override the default config with the libConfig
	if libConfig != nil {
		if libConfig.HttpRequest != nil {
			mergedConfig.HttpRequest = libConfig.HttpRequest
		}
		if libConfig.HttpResponse != nil {
			mergedConfig.HttpResponse = libConfig.HttpResponse
		}
		if libConfig.DbQuery != nil {
			mergedConfig.DbQuery = libConfig.DbQuery
		}
	}
	return mergedConfig
}

func readAndVerifyOffsetsFile(path string) (io.Reader, error) {
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(string(fileBytes), "---SIGNATURE---")
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

	hash := sha256.Sum256([]byte(originalContent))

	err = rsa.VerifyPKCS1v15(rsaPublicKey, crypto.SHA256, hash[:], signature)
	if err != nil {
		return nil, err
	}

	return bytes.NewReader([]byte(originalContent)), nil
}
