import React from 'react';
import styled from 'styled-components';
import { useConnectionsCRUD, useProxyID } from '@/hooks';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { CenterThis, FadeLoader } from '@odigos/ui-kit/components';
import { type Connection, StatusType } from '@odigos/ui-kit/types';
import { ComputePlatforms, ComputePlatformsActionsMenu } from '@odigos/ui-kit/containers';

const MainContent = styled.div`
  width: 95%;
  position: relative;
`;

const ConnectionsMain = () => {
  const { selectProxyID } = useProxyID();
  const { addNotification } = useNotificationStore();
  const { connections, loading } = useConnectionsCRUD();

  const onClickPlatform = (cp: Connection) => {
    if (cp.status === StatusType.Success) {
      selectProxyID(cp.id);
    } else {
      addNotification({
        type: StatusType.Warning,
        title: 'Connection Lost',
        message: 'Cannot connect to the platform',
        hideFromHistory: true,
      });
    }
  };

  return (
    <MainContent>
      <ComputePlatformsActionsMenu connections={connections} />

      {loading ? (
        <CenterThis>
          <FadeLoader />
        </CenterThis>
      ) : (
        <ComputePlatforms connections={connections} onSelect={onClickPlatform} />
      )}
    </MainContent>
  );
};

export { ConnectionsMain };
