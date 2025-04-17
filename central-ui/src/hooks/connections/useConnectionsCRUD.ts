import { useQuery } from '@apollo/client';
import { GET_CONNECTIONS } from '@/graphql';
import type { FetchedConnection } from '@/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { type Connection, Crud, StatusType, PlatformType } from '@odigos/ui-kit/types';

export const useConnectionsCRUD = (selectedId?: string) => {
  const { addNotification } = useNotificationStore();

  const notifyUser = (type: StatusType, title: string, message: string, hideFromHistory?: boolean) => {
    addNotification({ type, title, message, hideFromHistory });
  };

  const { data, loading } = useQuery<{ connections?: FetchedConnection[] }>(GET_CONNECTIONS, {
    onError: (error) => notifyUser(StatusType.Error, error.name || Crud.Read, error.cause?.message || error.message),
  });

  const connections: Connection[] =
    data?.connections?.map(({ proxyID, name, status }) => ({
      id: proxyID,
      type: PlatformType.K8s,
      name,
      status,
    })) || [];

  const getCurrentConnection = (id: string | undefined) => {
    return id ? connections.find((cp) => cp.id === id) : undefined;
  };

  return {
    loading,
    connections,
    currentConnection: getCurrentConnection(selectedId),
  };
};
