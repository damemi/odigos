package ebpf

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	odigosv1 "github.com/keyval-dev/odigos/api/odigos/v1alpha1"
	"github.com/keyval-dev/odigos/common"
	"github.com/keyval-dev/odigos/common/consts"
	commonutils "github.com/keyval-dev/odigos/common/utils"
	"github.com/keyval-dev/odigos/odiglet/pkg/env"
	"github.com/keyval-dev/odigos/odiglet/pkg/kube/utils"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
	"go.opentelemetry.io/auto"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type GoInstrumentationFactoryEnterprise struct {
	kubeclient client.Client
}

func NewGoInstrumentationFactoryEnterprise(kubeclient client.Client) InstrumentationFactoryEnterprise[*auto.Instrumentation] {
	return &GoInstrumentationFactoryEnterprise{
		kubeclient: kubeclient,
	}
}

func (g *GoInstrumentationFactoryEnterprise) CreateEbpfInstrumentation(ctx context.Context, pid int, serviceName string, podWorkload *common.PodWorkload) (*auto.Instrumentation, error) {
	defaultExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(fmt.Sprintf("%s:%d", env.Current.NodeIP, consts.OTLPPort)),
	)
	if err != nil {
		log.Logger.Error(err, "failed to create exporter")
		return nil, err
	}

	inst, err := auto.NewInstrumentation(
		ctx,
		auto.WithPID(pid),
		auto.WithResourceAttributes(utils.GetResourceAttributes(podWorkload)...),
		auto.WithServiceName(serviceName),
		auto.WithTraceExporter(defaultExporter),
	)
	if err != nil {
		log.Logger.Error(err, "instrumentation setup failed")
		return nil, err
	}

	// Fetch the InstrumentationConfig instrumentationConfig
	instrumentationConfig := &odigosv1.InstrumentationConfig{}
	instrumentationConfigKey := client.ObjectKey{
		Namespace: podWorkload.Namespace,
		Name:      commonutils.GetRuntimeObjectName(podWorkload.Name, podWorkload.Kind),
	}
	err = g.kubeclient.Get(ctx, instrumentationConfigKey, instrumentationConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return inst, nil
		}
		return nil, err
	}

	err = applyConfigToInst(&instrumentationConfig.Spec, inst, common.GoProgrammingLanguage)
	if err != nil {
		return nil, err
	}

	return inst, nil
}
