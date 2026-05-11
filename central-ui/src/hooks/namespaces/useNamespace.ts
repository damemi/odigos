import { useEffect } from 'react';
import { useConfig } from '../config';
import { useProxy, useRemoteFetch } from '../proxy';
import type { NamespaceInstrumentInput } from '@/types';
import { trimVersion } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { GET_NAMESPACE, GET_NAMESPACES, GET_NAMESPACES_WITH_WORKLOADS, PERSIST_NAMESPACES } from '@/graphql';
import { EntityTypes, type Namespace, type NamespaceWorkload, type Source, StatusType } from '@odigos/ui-kit/types';
import { useDataStreamStore, useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';

const V120 = trimVersion('v1.20');

interface GqlNamespaceWithWorkloads {
  name: string;
  markedForInstrumentation: boolean;
  dataStreamNames: string[];
  workloads: NamespaceWorkload[];
}

// `currentStreamName` scopes the `selected` flag to the active data stream. Without it,
// a workload instrumented in *any* stream renders pre-checked in another stream's
// add-source drawer (e.g. the add drawer for a brand-new stream would show every
// already-instrumented workload as already part of the new stream).
function transformNamespacesResponse(gqlNamespaces: GqlNamespaceWithWorkloads[], currentStreamName: string): Namespace[] {
  return gqlNamespaces.map((ns) => ({
    name: ns.name,
    selected: ns.markedForInstrumentation && ns.dataStreamNames.includes(currentStreamName),
    dataStreamNames: ns.dataStreamNames,
    sources: ns.workloads.map((w) => ({
      namespace: w.id.namespace,
      kind: w.id.kind,
      name: w.id.name,
      selected: (w.markedForInstrumentation.markedForInstrumentation ?? false) && w.dataStreamNames.includes(currentStreamName),
      dataStreamNames: w.dataStreamNames,
      numberOfInstances: w.numberOfInstances ?? undefined,
      otelServiceName: '',
      containers: null,
      conditions: null,
    })) as Source[],
  }));
}

export const useNamespace = () => {
  const { odigosVersion } = useProxy();
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { selectedStreamName } = useDataStreamStore();
  const { namespacesLoading, setEntitiesLoading, namespaces, setEntities } = useEntityStore();

  // @deprecated-legacy-api(v1.20) - Remove when min supported proxy >= v1.20
  // The isV120Plus check, legacy query hooks (queryAllNs, querySingleNs), and the
  // else branch in fetchNamespacesWithWorkloads are only needed for proxies < v1.20.
  const isV120Plus = odigosVersion ? trimVersion(odigosVersion) >= V120 : false;

  const notifyUser = (type: StatusType, title: string, message: string, hideFromHistory?: boolean) => {
    addNotification({ type, title, message, hideFromHistory });
  };

  // v1.20+ query
  const { fetch: queryNsWithWorkloads } = useRemoteFetch<{ namespaces: GqlNamespaceWithWorkloads[] }>(GET_NAMESPACES_WITH_WORKLOADS);

  // legacy queries (< v1.20)
  const { fetch: queryAllNs } = useRemoteFetch<{ computePlatform?: { k8sActualNamespaces?: Namespace[] } }>(GET_NAMESPACES);
  const { fetch: querySingleNs } = useRemoteFetch<{ computePlatform?: { k8sActualNamespace?: Namespace } }, { namespaceName: string }>(GET_NAMESPACE);

  const { fetch: mutatePersist } = useRemoteFetch<{ persistK8sNamespaces: boolean }, NamespaceInstrumentInput>(PERSIST_NAMESPACES);

  const fetchNamespacesWithWorkloads = async (): Promise<{ loading: boolean; error?: string; data?: Namespace[] }> => {
    if (isV120Plus) {
      const { data, error } = await queryNsWithWorkloads();

      if (error) {
        notifyUser(StatusType.Error, 'Error', error);
        return { loading: false, error };
      }

      if (data?.namespaces) {
        const transformed = transformNamespacesResponse(data.namespaces, selectedStreamName);

        const namespacesOnly = transformed.map(({ sources, ...ns }) => ns) as Namespace[];
        setEntities(EntityTypes.Namespace, namespacesOnly);

        return { loading: false, data: transformed };
      }

      return { loading: false };
    }

    // @deprecated-legacy-api(v1.20) - Remove this entire else branch
    // Legacy path: fetch all namespaces, then fetch each one individually to get sources
    setEntitiesLoading(EntityTypes.Namespace, true);

    const { data: allNsData, error: allNsError } = await queryAllNs();

    if (allNsError) {
      setEntitiesLoading(EntityTypes.Namespace, false);
      notifyUser(StatusType.Error, 'Error', allNsError);
      return { loading: false, error: allNsError };
    }

    const nsList = allNsData?.computePlatform?.k8sActualNamespaces;
    if (!nsList) {
      setEntitiesLoading(EntityTypes.Namespace, false);
      return { loading: false };
    }

    setEntities(EntityTypes.Namespace, nsList);

    const namespacesWithSources: Namespace[] = await Promise.all(
      nsList.map(async (ns) => {
        const { data: singleNsData } = await querySingleNs({ namespaceName: ns.name });
        const nsData = singleNsData?.computePlatform?.k8sActualNamespace;

        return {
          ...ns,
          sources: nsData?.sources || [],
        };
      }),
    );

    setEntitiesLoading(EntityTypes.Namespace, false);

    return { loading: false, data: namespacesWithSources };
  };

  const persistNamespaces = async (payload: NamespaceInstrumentInput) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, true);
    } else {
      await mutatePersist(payload);
    }
  };

  useEffect(() => {
    if (selectedStreamName && !namespaces.length && !namespacesLoading) fetchNamespacesWithWorkloads();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedStreamName]);

  return {
    namespacesLoading,
    namespaces,
    fetchNamespacesWithWorkloads,
    persistNamespaces,
  };
};
