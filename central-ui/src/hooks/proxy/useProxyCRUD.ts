import { useMutation, type ApolloError } from '@apollo/client';
import { DELETE_PROXY_CONNECTION } from '@/graphql';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import type { GeneralResult } from '@/types';

export const useProxyCRUD = () => {
  const { addNotification } = useNotificationStore();
  const onError = (error: ApolloError, crudType: Crud) => addNotification({ type: StatusType.Error, title: error.name || crudType, message: error.cause?.message || error.message });

  const [deleteProxyConnectionMutation, { loading: deleting }] = useMutation<{ deleteProxyConnection: GeneralResult }, { proxyID: string }>(DELETE_PROXY_CONNECTION, {
    onError: (error) => onError(error, Crud.Delete),
  });

  const deleteProxy = async (proxyID: string): Promise<GeneralResult | undefined> => {
    const { data } = await deleteProxyConnectionMutation({
      variables: { proxyID },
    });

    return data?.deleteProxyConnection;
  };

  return {
    deleteProxy,
    deleting,
  };
};
