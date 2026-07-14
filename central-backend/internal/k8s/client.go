package k8s

import (
	"context"
	"fmt"
	"os"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Client bundles a typed core clientset (for Secrets and other built-in
// resources) with a controller-runtime client whose scheme knows the odigos
// CRDs (for OdigosCloudConnector CRUD).
type Client struct {
	kubernetes.Interface
	Ctrl ctrlclient.Client
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
		return nil, fmt.Errorf("failed to create k8s config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register core scheme: %w", err)
	}
	if err := odigosv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register odigos scheme: %w", err)
	}
	ctrl, err := ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	return &Client{
		Interface: clientset,
		Ctrl:      ctrl,
	}, nil
}

func (c *Client) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	secret, err := c.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s in namespace %s: %w", name, namespace, err)
	}
	return secret, nil
}

func (c *Client) GetSecretData(ctx context.Context, namespace, name string) (map[string][]byte, error) {
	secret, err := c.GetSecret(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	return secret.Data, nil
}
