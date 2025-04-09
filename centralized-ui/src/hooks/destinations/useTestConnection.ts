import { useProxy } from '../proxy';
import { useConfig } from '../config';
import { TEST_DESTINATION_CONNECTION } from '@/graphql';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { type DestinationFormData, StatusType } from '@odigos/ui-kit/types';

interface TestConnectionResponse {
  succeeded: boolean;
  statusCode: number;
  destinationType: string;
  message: string;
  reason: string;
}

export const useTestConnection = () => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();

  const { fetch, loading: isTestConnectionLoading, data } = useProxy<{ testConnectionForDestination: TestConnectionResponse }>(TEST_DESTINATION_CONNECTION);

  const testConnection = async (destination: DestinationFormData) => {
    if (isReadonly) {
      addNotification({
        type: StatusType.Warning,
        title: DISPLAY_TITLES.READONLY,
        message: FORM_ALERTS.READONLY_WARNING,
        hideFromHistory: true,
      });
    } else {
      await fetch({ destination: { ...destination, fields: destination.fields.map((f) => ({ ...f, value: f.value || '' })) } });
    }
  };

  return {
    testConnection,
    isTestConnectionLoading,
    testConnectionResult: data?.testConnectionForDestination,
  };
};
