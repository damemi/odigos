import type { GeneralResult } from '@/types';
import { useMutation } from '@apollo/client/react';
import { DELETE_CLOUD_CONNECTOR, DELETE_PROXY_CONNECTION } from '@/graphql';
import { Crud, PlatformType, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';

/**
 * @deprecated Prefer `useOdigosApi().connectionsApi.deleteConnection` — the
 * kit owns connection teardown via the adapter's DELETE_PROXY_CONNECTION /
 * DELETE_CLOUD_CONNECTOR slots. Kept for any host code that still calls
 * Apollo directly outside the connections page.
 */
export const useProxyCRUD = () => {
  const { addNotification } = useNotificationStore();
  const notifyError = (error: { name?: string; message: string; cause?: unknown }, crudType: Crud) =>
    addNotification({ type: StatusType.Error, title: error.name || crudType, message: (error.cause instanceof Error ? error.cause.message : undefined) || error.message });

  const [deleteProxyConnectionMutation, { loading: deletingProxy }] = useMutation<{ deleteProxyConnection: GeneralResult }, { proxyID: string }>(DELETE_PROXY_CONNECTION);
  const [deleteCloudConnectorMutation, { loading: deletingConnector }] = useMutation<{ deleteCloudConnector: GeneralResult }, { connectorId: string }>(DELETE_CLOUD_CONNECTOR);

  const notifyDeleteResult = (result: GeneralResult | undefined) => {
    if (!result?.success) {
      addNotification({
        type: StatusType.Error,
        title: Crud.Delete,
        message: result?.error || 'Failed to delete connection',
      });
      return result;
    }
    addNotification({
      type: StatusType.Success,
      title: Crud.Delete,
      message: 'Connection deleted successfully',
    });
    return result;
  };

  const deleteProxy = async (proxyID: string): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await deleteProxyConnectionMutation({
        variables: { proxyID },
      });
      return notifyDeleteResult(data?.deleteProxyConnection);
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Delete);
    }
  };

  const deleteCloudConnector = async (connectorId: string): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await deleteCloudConnectorMutation({
        variables: { connectorId },
      });
      return notifyDeleteResult(data?.deleteCloudConnector);
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Delete);
    }
  };

  // Routes connection teardown by platform type. Cloud connectors are CR-backed
  // (deleteCloudConnector); k8s/vm/ecs proxies are Redis-backed (deleteProxy).
  const deleteConnection = async (id: string, type?: PlatformType): Promise<GeneralResult | undefined> => {
    if (type === PlatformType.Connector) {
      return deleteCloudConnector(id);
    }
    return deleteProxy(id);
  };

  return {
    deleteProxy,
    deleteCloudConnector,
    deleteConnection,
    deleting: deletingProxy || deletingConnector,
  };
};
