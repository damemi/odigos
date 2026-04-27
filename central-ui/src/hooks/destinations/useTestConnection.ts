import { useProxyStore } from '@/store';
import { useRemoteFetch } from '../proxy';
import { pickFallbackProxyID } from '@/utils';
import type { ComputePlatform } from '@/types';
import { TEST_DESTINATION_CONNECTION } from '@/graphql';
import type { DestinationFormData } from '@odigos/ui-kit/types';

interface TestConnectionResponse {
  succeeded: boolean;
  statusCode: number;
  destinationType: string;
  message: string;
  reason: string;
}

/**
 * @param connections Optional list of all known compute platforms. Used to
 * resolve a target proxy when the call is initiated from a page without a
 * selected proxy (e.g. the central connections page). Testing the connection
 * once against any working K8s cluster is sufficient — credentials are
 * destination-side, not cluster-side.
 */
export const useTestConnection = (connections: ComputePlatform[] = []) => {
  const { proxyID } = useProxyStore();

  const {
    fetch: testConnectionMutation,
    loading: isTestConnectionLoading,
    data,
  } = useRemoteFetch<{ testConnectionForDestination: TestConnectionResponse }, { destination: DestinationFormData }>(TEST_DESTINATION_CONNECTION, connections);

  // connectionIds is forwarded by the ui-kit's bulk destination drawer when
  // multiple clusters are selected; we only need a single working cluster to
  // run the test against, so pick the first id that maps to a known connection.
  const testConnection = async (destination: DestinationFormData, connectionIds?: string[]) => {
    const targetProxyID = proxyID || connectionIds?.find((id) => connections.some((c) => c.id === id)) || pickFallbackProxyID(connections);
    const { data } = await testConnectionMutation({ destination: { ...destination, fields: destination.fields.map((f) => ({ ...f, value: f.value || '' })) } }, targetProxyID);

    return data?.testConnectionForDestination;
  };

  return {
    testConnection,
    isTestConnectionLoading,
    testConnectionResult: data?.testConnectionForDestination,
  };
};
