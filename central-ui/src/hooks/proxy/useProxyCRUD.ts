import type { GeneralResult } from '@/types';
import { useMutation } from '@apollo/client/react';
import { DELETE_PROXY_CONNECTION } from '@/graphql';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';

export const useProxyCRUD = () => {
  const { addNotification } = useNotificationStore();
  const notifyError = (error: { name?: string; message: string; cause?: unknown }, crudType: Crud) =>
    addNotification({ type: StatusType.Error, title: error.name || crudType, message: (error.cause instanceof Error ? error.cause.message : undefined) || error.message });

  const [deleteProxyConnectionMutation, { loading: deleting }] = useMutation<{ deleteProxyConnection: GeneralResult }, { proxyID: string }>(DELETE_PROXY_CONNECTION);

  const deleteProxy = async (proxyID: string): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await deleteProxyConnectionMutation({
        variables: { proxyID },
      });
      return data?.deleteProxyConnection;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Delete);
    }
  };

  return {
    deleteProxy,
    deleting,
  };
};
