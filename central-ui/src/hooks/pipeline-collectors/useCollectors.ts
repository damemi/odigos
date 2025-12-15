import { useProxy } from '../proxy';
import { GET_GATEWAY_INFO, GET_GATEWAY_PODS, GET_NODE_COLLECTOR_INFO, GET_NODE_COLLECTOR_PODS, GET_COLLECTOR_POD_INFO } from '@/graphql';
import {
  type GatewayInfo,
  type NodeCollectoInfo,
  type PodInfo,
  type ExtendedPodInfo,
  type GetGatewayInfo,
  type GetGatewayPods,
  type GetNodeCollectorInfo,
  type GetNodeCollectorPods,
  type GetExtendedPodInfo,
} from '@odigos/ui-kit/types';

interface UseCollectorsResult {
  getGatewayInfo: GetGatewayInfo;
  getGatewayPods: GetGatewayPods;
  getNodeCollectorInfo: GetNodeCollectorInfo;
  getNodeCollectorPods: GetNodeCollectorPods;
  getExtendedPodInfo: GetExtendedPodInfo;
}

export const useCollectors = (): UseCollectorsResult => {
  const { fetch: getGatewayInfo } = useProxy<{ gatewayDeploymentInfo?: GatewayInfo }, {}>(GET_GATEWAY_INFO);
  const { fetch: getGatewayPods } = useProxy<{ gatewayPods?: PodInfo[] }, {}>(GET_GATEWAY_PODS);
  const { fetch: getNodeCollectorInfo } = useProxy<{ odigletDaemonSetInfo?: NodeCollectoInfo }, {}>(GET_NODE_COLLECTOR_INFO);
  const { fetch: getNodeCollectorPods } = useProxy<{ odigletPods?: PodInfo[] }, {}>(GET_NODE_COLLECTOR_PODS);
  const { fetch: getExtendedPodInfo } = useProxy<{ collectorPod?: ExtendedPodInfo }, { namespace: string; name: string }>(GET_COLLECTOR_POD_INFO);

  return {
    getGatewayInfo: () => getGatewayInfo().then((result) => result.data?.gatewayDeploymentInfo),
    getGatewayPods: () => getGatewayPods().then((result) => result.data?.gatewayPods),
    getNodeCollectorInfo: () => getNodeCollectorInfo().then((result) => result.data?.odigletDaemonSetInfo),
    getNodeCollectorPods: () => getNodeCollectorPods().then((result) => result.data?.odigletPods),
    getExtendedPodInfo: (namespace: string, name: string) => getExtendedPodInfo({ namespace, name }).then((result) => result.data?.collectorPod),
  };
};
