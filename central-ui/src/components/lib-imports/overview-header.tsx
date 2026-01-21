import React, { useMemo } from 'react';
import { AccountMenu } from '../account-menu';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { StatusKeys, useStatusStore } from '../../store';
import { Header, Tooltip } from '@odigos/ui-kit/components';
import { OdigosCentralLogoText } from '@odigos/ui-kit/icons';
import { OtherStatusType, StatusType } from '@odigos/ui-kit/types';
import { useConfig, useDescribe, useHealth, useProxy, useTokenCRUD } from '@/hooks';
import { Badge as V2Badge, Header as V2Header } from '@odigos/ui-kit/components/v2';
import { NotificationManager, SlackInvite, SystemOverview, ToggleDarkMode } from '@odigos/ui-kit/containers';

const OverviewHeader = ({ v2 }: { v2?: boolean }) => {
  useHealth();

  const { isVm } = useOdigos();
  const { isReadonly } = useConfig();
  const { proxyID, proxyName } = useProxy();
  const { fetchDescribeOdigos } = useDescribe();
  const { tokens, updateToken } = useTokenCRUD();

  const tokenStatus = useStatusStore((state) => state[StatusKeys.Token]);
  const backendStatus = useStatusStore((state) => state[StatusKeys.Backend]);
  const instrumentationStatus = useStatusStore((state) => state[StatusKeys.Instrumentation]);

  const left = useMemo(() => {
    const arr = [<OdigosCentralLogoText key='logo' size={150} />];

    if (proxyName) {
      arr.push(<V2Badge key='proxy-name' status={StatusType.Default} label={proxyName} />);
    }
    if (tokenStatus) {
      arr.push(
        <Tooltip key='token-status' text={tokenStatus.tooltip}>
          <V2Badge {...tokenStatus} />
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
    if (isReadonly) {
      arr.push(
        <Tooltip key='readonly' text={FORM_ALERTS.READONLY_WARNING}>
          <V2Badge status={OtherStatusType.Disabled} label='Read Only' />
        </Tooltip>,
      );
    }

    return arr;
  }, [v2, tokenStatus?.label, backendStatus?.label, instrumentationStatus?.label, isReadonly]);

  const right = useMemo(() => {
    const arr = [<NotificationManager key='notification-manager' />];
    if (!v2) arr.unshift(<ToggleDarkMode key='toggle-theme' />);
    if (!isVm && proxyID) arr.push(<SystemOverview key='system-overview' tokens={tokens} saveToken={updateToken} fetchDescribeOdigos={fetchDescribeOdigos} />);
    arr.push(...[<SlackInvite key='slack-invite' />, <AccountMenu key='account-menu' />]);

    return arr;
  }, [v2, isVm, proxyID, proxyName, tokens]);

  if (v2) {
    return <V2Header left={left} right={right} />;
  }

  return <Header left={left} right={right} />;
};

export { OverviewHeader };
