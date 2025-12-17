import { useRemoteFetch } from '../proxy';
import { GET_DESCRIBE_ODIGOS, GET_DESCRIBE_SOURCE } from '@/graphql';
import type { DescribeOdigos, DescribeSource } from '@odigos/ui-kit/types';

export const useDescribe = () => {
  const { fetch: fetchDescribeOdigos } = useRemoteFetch<{ describeOdigos: DescribeOdigos }>(GET_DESCRIBE_ODIGOS);
  const { fetch: fetchDescribeSource } = useRemoteFetch<{ describeSource: DescribeSource }>(GET_DESCRIBE_SOURCE);

  return {
    fetchDescribeOdigos,
    fetchDescribeSource,
  };
};
