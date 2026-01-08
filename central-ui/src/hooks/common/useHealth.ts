import { useEffect } from 'react';
import { GET_HEALTH } from '@/graphql';
import { useQuery } from '@apollo/client';
import { useTheme } from 'styled-components';
import { StatusType } from '@odigos/ui-kit/types';
import { getStatusIcon } from '@odigos/ui-kit/functions';
import { StatusKeys, StatusValues, useStatusStore } from '@/store';

enum HealthStatus {
  HEALTHY = 'HEALTHY',
  WARNING = 'WARNING',
  DOWN = 'DOWN',
}

interface ServicesStatus {
  status: HealthStatus;
  message?: string | null;
}

interface BackendHealthStatus {
  redisStatus: ServicesStatus;
  keycloakStatus: ServicesStatus;
  sseStatus: ServicesStatus;
}

interface HealthQuery {
  backend: BackendHealthStatus;
}

export const useHealth = () => {
  const theme = useTheme();
  const { setStatusStore } = useStatusStore();

  const { data } = useQuery<{ health: HealthQuery }>(GET_HEALTH, {
    pollInterval: 60000,
  });

  useEffect(() => {
    if (data) {
      const { backend } = data.health;

      const statusPayload: StatusValues = {
        status: StatusType.Success,
        label: 'Connection is healthy',
        tooltip: 'Connection with Redis, Keycloak and SSE are healthy',
        invertColors: true,
      };

      let errorMessage = '';
      let warningMessage = '';

      for (const [key, value] of Object.entries(backend)) {
        if (value.status === HealthStatus.DOWN) {
          errorMessage += `The "${key.replace('Status', '').toLowerCase()}" service is down. ${value.message ?? 'No message provided'}\n`;
        } else if (value.status === HealthStatus.WARNING) {
          warningMessage += `The "${key.replace('Status', '').toLowerCase()}" service is in warning state. ${value.message ?? 'No message provided'}\n`;
        }
      }

      if (errorMessage) {
        statusPayload.status = StatusType.Error;
        statusPayload.label = 'Connection is down';
        statusPayload.tooltip = errorMessage;
      } else if (warningMessage) {
        statusPayload.status = StatusType.Warning;
        statusPayload.label = 'Connection has warnings';
        statusPayload.tooltip = warningMessage;
      }

      statusPayload.leftIcon = getStatusIcon(statusPayload.status, theme);

      setStatusStore(StatusKeys.Backend, statusPayload);
    }
  }, [data, theme]);

  return null;
};
