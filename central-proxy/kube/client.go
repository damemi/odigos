package kube

import (
	"os"

	odigosclientset "github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	kubernetes.Interface
	OdigosClient odigosclientset.Interface
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

	return &Client{
		Interface:    clientset,
		OdigosClient: odigosClient,
	}, nil
}
