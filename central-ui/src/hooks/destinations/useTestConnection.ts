import { useProxy } from '../proxy';
import { TEST_DESTINATION_CONNECTION } from '@/graphql';
import { type DestinationFormData } from '@odigos/ui-kit/types';

interface TestConnectionResponse {
  succeeded: boolean;
  statusCode: number;
  destinationType: string;
  message: string;
  reason: string;
}

export const useTestConnection = () => {
  const {
    fetch: testConnectionMutation,
    loading: isTestConnectionLoading,
    data,
  } = useProxy<{ testConnectionForDestination: TestConnectionResponse }, { destination: DestinationFormData }>(TEST_DESTINATION_CONNECTION);

  const testConnection = async (destination: DestinationFormData) => {
    const { data } = await testConnectionMutation({ destination: { ...destination, fields: destination.fields.map((f) => ({ ...f, value: f.value || '' })) } });

    return data?.testConnectionForDestination;
  };

  return {
    testConnection,
    isTestConnectionLoading,
    testConnectionResult: data?.testConnectionForDestination,
  };
};
