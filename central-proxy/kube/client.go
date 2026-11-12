package kube

import (
	"context"
	"os"

	"github.com/odigos-io/odigos/central-proxy/internal/logger"
	odigosclientset "github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var deploymentConfigGVR = schema.GroupVersionResource{
	Group:    "apps.openshift.io",
	Version:  "v1",
	Resource: "deploymentconfigs",
}

type Client struct {
	kubernetes.Interface
	OdigosClient  odigosclientset.Interface
	DynamicClient dynamic.Interface
}

func CreateClient() (*Client, error) {
	var config *rest.Config
	var err error
	if _, inCluster := os.LookupEnv("KUBERNETES_SERVICE_HOST"); inCluster {
		config, err = rest.InClusterConfig()
	} else {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = clientcmd.RecommendedHomeFile
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	odigosClient, err := odigosclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &Client{
		Interface:     clientset,
		OdigosClient:  odigosClient,
		DynamicClient: dynamicClient,
	}, nil
}

func (c *Client) IsDeploymentConfigAvailable(ctx context.Context) bool {
	if c.DynamicClient == nil {
		return false
	}

	_, err := c.DynamicClient.Resource(deploymentConfigGVR).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		logger.Log.Info("DeploymentConfig snapshots disabled", "error", err)
		return false
	}

	return true
}
