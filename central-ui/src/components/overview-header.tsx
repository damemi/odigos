'use client';

import React, { useMemo, useState } from 'react';
import { useTheme } from 'styled-components';
import { AccountMenu } from './account-menu';
import { StatusType } from '@odigos/ui-kit/types';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { StatusKeys, useStatusStore } from '../store';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { SystemDrawer } from '@odigos/ui-kit/containers';
import { useConfig, useHealth, useProxy } from '@/hooks';
import { OdigosLogoTextByTier } from '@odigos/ui-kit/snippets';
import { VmDiagnoseDrawer } from '@/components/vm-diagnose-drawer';
import { ExclamationTriangleIcon, TerminalIcon } from '@odigos/ui-kit/icons';
import { ButtonVariants, Tooltip, WarningModal } from '@odigos/ui-kit/components';
import { IconButton, Badge as V2Badge, Header as V2Header } from '@odigos/ui-kit/components';

export const OverviewHeader = () => {
  useHealth();

  const theme = useTheme();
  const { isVm } = useOdigos();
  const { isReadonly } = useConfig();
  const { proxyID, proxyName } = useProxy();

  const [isSystemDrawerOpen, setIsSystemDrawerOpen] = useState(false);
  const toggleSystemDrawer = () => setIsSystemDrawerOpen((prev) => !prev);

  const tokenStatus = useStatusStore((state) => state[StatusKeys.Token]);
  const backendStatus = useStatusStore((state) => state[StatusKeys.Backend]);
  const instrumentationStatus = useStatusStore((state) => state[StatusKeys.Instrumentation]);

  const left = useMemo(() => {
    const arr = [<OdigosLogoTextByTier key='logo' size={200} central />];

    if (proxyName) {
      arr.push(<V2Badge key='proxy-name' status={StatusType.Default} label={proxyName} />);
    }
    if (isReadonly) {
      arr.push(
        <Tooltip key='readonly' text={FORM_ALERTS.READONLY_WARNING}>
          <V2Badge status={StatusType.Disabled} label='Read Only' />
        </Tooltip>,
      );
    }
    if (backendStatus) {
      arr.push(
        <Tooltip key='backend-status' text={backendStatus.tooltip}>
          <V2Badge {...backendStatus} />
        </Tooltip>,
      );
    }
    if (instrumentationStatus) {
      arr.push(
        <Tooltip key='instrumentation-status' text={instrumentationStatus.tooltip}>
          <V2Badge {...instrumentationStatus} />
        </Tooltip>,
      );
    }

    return arr;
  }, [proxyName, isReadonly, backendStatus, instrumentationStatus]);

  const right = useMemo(() => {
    const arr = [];

    if (tokenStatus) {
      arr.push(
        <Tooltip key='token-status' text={tokenStatus.tooltip}>
          <V2Badge {...tokenStatus} />
        </Tooltip>,
      );
    }
    if (proxyID) {
      arr.push(<IconButton key='system-drawer' icon={TerminalIcon} onClick={toggleSystemDrawer} />);
    }
    arr.push(<AccountMenu key='account-menu' />);

    return arr;
  }, [proxyID, tokenStatus]);

  return (
    <>
      <V2Header left={left} right={right} />
      {isVm ? <VmDiagnoseDrawer isOpen={isSystemDrawerOpen} onClose={toggleSystemDrawer} /> : <SystemDrawer key='system-overview' isOpen={isSystemDrawerOpen} onClose={toggleSystemDrawer} />}

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
