import { useEffect } from 'react';
import { useConfig } from '../config';
import { useProxy, useRemoteFetch } from '../proxy';
import { trimVersion } from '@odigos/ui-kit/functions';
import type { NamespaceInstrumentInput } from '@/types';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { useDataStreamStore, useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';
import { GET_NAMESPACE, GET_NAMESPACES, GET_NAMESPACES_WITH_WORKLOADS, PERSIST_NAMESPACES } from '@/graphql';
import { EntityTypes, type K8sResourceKind, type Namespace, type NamespaceWorkload, StatusType } from '@odigos/ui-kit/types';

const V120 = trimVersion('v1.20');

interface GqlNamespaceWithWorkloads {
  name: string;
  markedForInstrumentation: boolean;
  dataStreamNames: string[];
  workloads: NamespaceWorkload[];
}

// @deprecated-legacy-api(v1.20) - legacy `K8sActualNamespace` shape returned
// by proxies < v1.20. Uses `selected` instead of `markedForInstrumentation`
// and a flat `sources` field instead of nested `workloads`.
interface LegacyK8sActualSource {
  namespace: string;
  kind: string;
  name: string;
  dataStreamNames: string[];
  selected: boolean;
  numberOfInstances: number | null;
}

interface LegacyK8sActualNamespace {
  name: string;
  selected: boolean;
  dataStreamNames: string[];
  sources?: LegacyK8sActualSource[];
}

// @deprecated-legacy-api(v1.20) - converts the legacy `K8sActualNamespace`
// shape into the v1.20+ `Namespace` shape that the ui-kit now consumes.
function legacyNamespaceToNamespace(ns: LegacyK8sActualNamespace): Namespace {
  return {
    name: ns.name,
    markedForInstrumentation: ns.selected,
    dataStreamNames: ns.dataStreamNames,
    workloads: ns.sources?.map((s) => ({
      id: { namespace: s.namespace, kind: s.kind as K8sResourceKind | '', name: s.name },
      markedForInstrumentation: { markedForInstrumentation: s.selected },
      dataStreamNames: s.dataStreamNames,
      numberOfInstances: s.numberOfInstances,
    })),
  };
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

  // legacy queries (< v1.20) - return the flat `K8sActualNamespace` shape, which is
  // adapted to the v1.20+ `Namespace` shape via `legacyNamespaceToNamespace`.
  const { fetch: queryAllNs } = useRemoteFetch<{ computePlatform?: { k8sActualNamespaces?: LegacyK8sActualNamespace[] } }>(GET_NAMESPACES);
  const { fetch: querySingleNs } = useRemoteFetch<{ computePlatform?: { k8sActualNamespace?: LegacyK8sActualNamespace } }, { namespaceName: string }>(GET_NAMESPACE);

  const { fetch: mutatePersist } = useRemoteFetch<{ persistK8sNamespaces: boolean }, NamespaceInstrumentInput>(PERSIST_NAMESPACES);

  const fetchNamespacesWithWorkloads = async (): Promise<{ loading: boolean; error?: string; data?: Namespace[] }> => {
    if (isV120Plus) {
      const { data, error } = await queryNsWithWorkloads();

      if (error) {
        notifyUser(StatusType.Error, 'Error', error);
        return { loading: false, error };
      }

      if (data?.namespaces) {
        const namespacesOnly = data.namespaces.map(({ workloads, ...ns }) => ns) as Namespace[];
        setEntities(EntityTypes.Namespace, namespacesOnly);

        return { loading: false, data: data.namespaces };
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

    const mappedNsList = nsList.map(legacyNamespaceToNamespace);
    setEntities(EntityTypes.Namespace, mappedNsList);

    const namespacesWithSources: Namespace[] = await Promise.all(
      nsList.map(async (ns) => {
        const { data: singleNsData } = await querySingleNs({ namespaceName: ns.name });
        const nsData = singleNsData?.computePlatform?.k8sActualNamespace;

        return legacyNamespaceToNamespace({
          ...ns,
          sources: nsData?.sources || [],
        });
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
