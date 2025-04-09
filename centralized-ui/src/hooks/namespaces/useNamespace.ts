import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import type { NamespaceInstrumentInput } from '@/types';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';
import { GET_NAMESPACE, GET_NAMESPACES, PERSIST_NAMESPACES } from '@/graphql';
import { EntityTypes, type Namespace, StatusType } from '@odigos/ui-kit/types';

export const useNamespace = (namespaceName?: string) => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { namespacesLoading, setEntitiesLoading, namespaces, setEntities } = useEntityStore();

  const notifyUser = (type: StatusType, title: string, message: string, hideFromHistory?: boolean) => {
    addNotification({ type, title, message, hideFromHistory });
  };

  const { fetch: fetchAll } = useProxy<{ computePlatform?: { k8sActualNamespaces?: Namespace[] } }>(GET_NAMESPACES);
  const { fetch: fetchSingle, data: singleNamespace, loading: singleLoading } = useProxy<{ computePlatform?: { k8sActualNamespace?: Namespace } }>(GET_NAMESPACE);
  const { fetch: mutatePersist } = useProxy<{ persistK8sNamespace: boolean }>(PERSIST_NAMESPACES);

  const fetchNamespaces = async () => {
    setEntitiesLoading(EntityTypes.Namespace, true);
    const { data } = await fetchAll();

    if (data?.computePlatform?.k8sActualNamespaces) {
      const { k8sActualNamespaces: items } = data.computePlatform;
      setEntities(EntityTypes.Namespace, items);
      setEntitiesLoading(EntityTypes.Namespace, false);
    }
  };

  const persistNamespace = async (payload: NamespaceInstrumentInput) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, true);
    } else {
      await mutatePersist({ namespace: payload });
    }
  };

  useEffect(() => {
    if (!namespaces.length) fetchNamespaces();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (namespaceName && !singleLoading) fetchSingle({ namespaceName });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [namespaceName]);

  return {
    loading: namespacesLoading || singleLoading,
    namespaces,
    namespace: singleNamespace?.computePlatform?.k8sActualNamespace,
    persistNamespace,
  };
};
