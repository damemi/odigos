package k8s

import (
	"context"
	"fmt"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// UpsertSecret creates an Opaque Secret in the given namespace, or updates it in
// place if it already exists. data is stored as StringData so callers pass plain
// (unencoded) values; on update the existing Data is cleared first so the Secret
// reflects exactly the supplied credentials (no stale keys from a prior set).
func (c *Client) UpsertSecret(ctx context.Context, namespace, name string, data map[string]string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: data,
	}
	_, err := c.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := c.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("failed to get existing secret %s in namespace %s: %w", name, namespace, getErr)
		}
		existing.Type = corev1.SecretTypeOpaque
		existing.Data = nil
		existing.StringData = data
		if _, updErr := c.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{}); updErr != nil {
			return fmt.Errorf("failed to update secret %s in namespace %s: %w", name, namespace, updErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to create secret %s in namespace %s: %w", name, namespace, err)
	}
	return nil
}

// UpsertCloudConnector creates an OdigosCloudConnector CR, or updates its spec in
// place if one with the same name already exists. Connectors are keyed by
// provider+account, so re-submitting the same account updates the existing
// connector rather than failing on a name conflict.
func (c *Client) UpsertCloudConnector(ctx context.Context, cc *odigosv1.OdigosCloudConnector) error {
	if c.Ctrl == nil {
		return fmt.Errorf("controller-runtime client is not initialized")
	}
	err := c.Ctrl.Create(ctx, cc)
	if apierrors.IsAlreadyExists(err) {
		var existing odigosv1.OdigosCloudConnector
		if getErr := c.Ctrl.Get(ctx, ctrlclient.ObjectKey{Namespace: cc.Namespace, Name: cc.Name}, &existing); getErr != nil {
			return fmt.Errorf("failed to get existing OdigosCloudConnector %s/%s: %w", cc.Namespace, cc.Name, getErr)
		}
		existing.Spec = cc.Spec
		if updErr := c.Ctrl.Update(ctx, &existing); updErr != nil {
			return fmt.Errorf("failed to update OdigosCloudConnector %s/%s: %w", cc.Namespace, cc.Name, updErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to create OdigosCloudConnector %s/%s: %w", cc.Namespace, cc.Name, err)
	}
	return nil
}

// ListCloudConnectors lists all OdigosCloudConnector CRs in the given namespace.
func (c *Client) ListCloudConnectors(ctx context.Context, namespace string) ([]odigosv1.OdigosCloudConnector, error) {
	if c.Ctrl == nil {
		return nil, fmt.Errorf("controller-runtime client is not initialized")
	}
	var list odigosv1.OdigosCloudConnectorList
	if err := c.Ctrl.List(ctx, &list, ctrlclient.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list OdigosCloudConnectors in namespace %s: %w", namespace, err)
	}
	return list.Items, nil
}
