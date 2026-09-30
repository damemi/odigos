'use client';

import React, { useMemo, useState } from 'react';
import { AccountMenu } from './account-menu';
import { StatusType } from '@odigos/ui-kit/types';
import { TerminalIcon } from '@odigos/ui-kit/icons';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { Tooltip } from '@odigos/ui-kit/components';
import { StatusKeys, useStatusStore } from '../store';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { SystemDrawer } from '@odigos/ui-kit/containers';
import { VmDiagnoseDrawer } from '@/components/vm-diagnose-drawer';
import { useConfig, useHealth, useProxy } from '@/hooks';
import { OdigosLogoTextByTier } from '@odigos/ui-kit/snippets';
import { IconButton, Badge as V2Badge, Header as V2Header } from '@odigos/ui-kit/components';

export const OverviewHeader = () => {
  useHealth();

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
      {isVm ? (
        <VmDiagnoseDrawer isOpen={isSystemDrawerOpen} onClose={toggleSystemDrawer} />
      ) : (
        <SystemDrawer key='system-overview' isOpen={isSystemDrawerOpen} onClose={toggleSystemDrawer} />
      )}
    </>
  );
};
