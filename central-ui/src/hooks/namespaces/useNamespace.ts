import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import type { NamespaceInstrumentInput } from '@/types';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { GET_NAMESPACE, GET_NAMESPACES, PERSIST_NAMESPACES } from '@/graphql';
import { EntityTypes, type Namespace, StatusType } from '@odigos/ui-kit/types';
import { useDataStreamStore, useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';

export const useNamespace = () => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { selectedStreamName } = useDataStreamStore();
  const { namespacesLoading, setEntitiesLoading, namespaces, setEntities } = useEntityStore();

  const notifyUser = (type: StatusType, title: string, message: string, hideFromHistory?: boolean) => {
    addNotification({ type, title, message, hideFromHistory });
  };

  const { fetch: queryAllNs } = useProxy<{ computePlatform?: { k8sActualNamespaces?: Namespace[] } }>(GET_NAMESPACES);
  const { fetch: querySingleNs } = useProxy<{ computePlatform?: { k8sActualNamespace?: Namespace } }, { namespaceName: string }>(GET_NAMESPACE);
  const { fetch: mutatePersist } = useProxy<{ persistK8sNamespaces: boolean }, NamespaceInstrumentInput>(PERSIST_NAMESPACES);

  const fetchNamespaces = async () => {
    setEntitiesLoading(EntityTypes.Namespace, true);
    const { data } = await queryAllNs();

    if (data?.computePlatform?.k8sActualNamespaces) {
      const { k8sActualNamespaces: items } = data.computePlatform;
      setEntities(EntityTypes.Namespace, items);
      setEntitiesLoading(EntityTypes.Namespace, false);
    }
  };

  const persistNamespaces = async (payload: NamespaceInstrumentInput) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, true);
    } else {
      await mutatePersist(payload);
    }
  };

  useEffect(() => {
    if (selectedStreamName && !namespaces.length && !namespacesLoading) fetchNamespaces();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedStreamName]);

  return {
    namespacesLoading,
    namespaces,
    fetchNamespaces,
    fetchNamespace: querySingleNs,
    persistNamespaces,
  };
};
