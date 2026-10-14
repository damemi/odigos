package k8s

import (
	"context"
	"fmt"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
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
func (c *Client) UpsertCloudConnector(ctx context.Context, cc *cloudconnectorsv1.OdigosCloudConnector) error {
	if c.Ctrl == nil {
		return fmt.Errorf("controller-runtime client is not initialized")
	}
	err := c.Ctrl.Create(ctx, cc)
	if apierrors.IsAlreadyExists(err) {
		var existing cloudconnectorsv1.OdigosCloudConnector
		if getErr := c.Ctrl.Get(ctx, ctrlclient.ObjectKey{Namespace: cc.Namespace, Name: cc.Name}, &existing); getErr != nil {
			return fmt.Errorf("failed to get existing OdigosCloudConnector %s/%s: %w", cc.Namespace, cc.Name, getErr)
		}
		// Older clients omit collectors. Keep an explicit allow-list already set
		// on the CR instead of silently clearing it during credential updates.
		if cc.Spec.Collectors == nil {
			cc.Spec.Collectors = existing.Spec.Collectors
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
func (c *Client) ListCloudConnectors(ctx context.Context, namespace string) ([]cloudconnectorsv1.OdigosCloudConnector, error) {
	if c.Ctrl == nil {
		return nil, fmt.Errorf("controller-runtime client is not initialized")
	}
	var list cloudconnectorsv1.OdigosCloudConnectorList
	if err := c.Ctrl.List(ctx, &list, ctrlclient.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list OdigosCloudConnectors in namespace %s: %w", namespace, err)
	}
	return list.Items, nil
}

// UpdateCloudConnectorCollectors changes only the Collector allow-list. An
// allocated empty slice explicitly clears the previous selection.
func (c *Client) UpdateCloudConnectorCollectors(ctx context.Context, namespace, name string, collectors []cloudconnectorsv1.CloudConnectorCollectorCapability) error {
	if c.Ctrl == nil {
		return fmt.Errorf("controller-runtime client is not initialized")
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cc cloudconnectorsv1.OdigosCloudConnector
		if err := c.Ctrl.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: name}, &cc); err != nil {
			return err
		}
		cc.Spec.Collectors = append(make([]cloudconnectorsv1.CloudConnectorCollectorCapability, 0), collectors...)
		// The compatibility annotation is consulted when spec.collectors is empty.
		// Clear it as part of an explicit edit so Off cannot revive an old mode.
		delete(cc.Annotations, "odigos.io/collectors-capability")
		return c.Ctrl.Update(ctx, &cc)
	})
}

// DeleteCloudConnector deletes an OdigosCloudConnector CR by name.
func (c *Client) DeleteCloudConnector(ctx context.Context, namespace, name string) error {
	if c.Ctrl == nil {
		return fmt.Errorf("controller-runtime client is not initialized")
	}
	cc := &cloudconnectorsv1.OdigosCloudConnector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	if err := c.Ctrl.Delete(ctx, cc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete OdigosCloudConnector %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeleteSecret deletes a Secret by name. Missing secrets are treated as success
// so callers can clean up credentials after the CR without failing if the
// Secret was already removed.
func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	err := c.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete secret %s in namespace %s: %w", name, namespace, err)
	}
	return nil
}
