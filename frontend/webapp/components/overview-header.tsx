import React from 'react';
import { useTheme } from 'styled-components';
import { useConfig } from '@/hooks';
import { StatusType } from '@odigos/ui-kit/types';
import { StatusKeys, useStatusStore } from '../store';
import { AppHeader } from '@odigos/ui-kit/containers';
import { ExclamationTriangleIcon } from '@odigos/ui-kit/icons';
import { ButtonVariants, WarningModal } from '@odigos/ui-kit/components';

// PLAT-1561 spike (local only): the header is now the kit's AppHeader; the host only passes its data.
export const OverviewHeader = () => {
  const theme = useTheme();
  const { isReadonly } = useConfig();
  const tokenStatus = useStatusStore((state) => state[StatusKeys.Token]);

  return (
    <>
      <AppHeader isReadonly={isReadonly} statusBadges={[]} rightBadges={tokenStatus ? [{ id: 'token-status', ...tokenStatus }] : []} />

      {tokenStatus && !tokenStatus.modalDismissed && (
        <WarningModal
          isOpen
          onClose={() => useStatusStore.getState().dismissModal(StatusKeys.Token)}
          visual={() => <ExclamationTriangleIcon size={50} fill={tokenStatus.status === StatusType.Warning ? theme.v2.colors.yellow[500] : theme.v2.colors.red[500]} />}
          title={tokenStatus.status === StatusType.Warning ? 'Token about to expire' : 'Token expired'}
          description={tokenStatus.label?.toString() || ''}
          denyButton={{
            label: 'Dismiss',
            variant: ButtonVariants.Secondary,
            onClick: () => useStatusStore.getState().dismissModal(StatusKeys.Token),
          }}
        />
      )}
    </>
  );
};
