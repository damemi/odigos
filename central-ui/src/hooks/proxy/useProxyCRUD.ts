import { useProxy } from './useProxy';
import { DELETE_CENTRAL_PROXY } from '@/graphql';

interface DeleteCentralProxyResponse {
  deleteCentralProxy: boolean;
}

export const useProxyCRUD = () => {
  const { fetch, loading, data } = useProxy<DeleteCentralProxyResponse, Record<string, never>>(DELETE_CENTRAL_PROXY);

  const deleteProxy = async (proxyID: string) => {
    const { data } = await fetch({}, proxyID);
    return data?.deleteCentralProxy;
  };

  return {
    deleteProxy,
    deleting: loading,
    deleteResult: data?.deleteCentralProxy,
  };
};


