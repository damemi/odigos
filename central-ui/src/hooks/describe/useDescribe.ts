import { useProxy } from '../proxy';
import { GET_DESCRIBE_ODIGOS, GET_DESCRIBE_SOURCE } from '@/graphql';
import type { DescribeOdigos, DescribeSource } from '@odigos/ui-kit/types';

export const useDescribe = () => {
  const { fetch: fetchDescribeOdigos } = useProxy<{ describeOdigos: DescribeOdigos }>(GET_DESCRIBE_ODIGOS);
  const { fetch: fetchDescribeSource } = useProxy<{ describeSource: DescribeSource }>(GET_DESCRIBE_SOURCE);

  return {
    fetchDescribeOdigos,
    fetchDescribeSource,
  };
};
