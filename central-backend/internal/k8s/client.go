package k8s

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	kubernetes.Interface
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

	return &Client{
		Interface: clientset,
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
