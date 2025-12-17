import React, { useMemo } from 'react';
import { useRouter } from 'next/navigation';
import { ROUTES } from '@/utils';
import { useStatusStore } from '../../store';
import { AccountMenu } from '../account-menu';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { ConnectionSelect } from '../connection-select';
import { OdigosCentralLogoText } from '@odigos/ui-kit/icons';
import { OtherStatusType, StatusType } from '@odigos/ui-kit/types';
import { Header, Status, Tooltip } from '@odigos/ui-kit/components';
import { useConfig, useDescribe, useProxy, useTokenCRUD } from '@/hooks';
import { Badge as V2Badge, Header as V2Header } from '@odigos/ui-kit/components/v2';
import { NotificationManager, SlackInvite, SystemOverview, ToggleDarkMode } from '@odigos/ui-kit/containers';

const OverviewHeader = ({ v2 }: { v2?: boolean }) => {
  const router = useRouter();
  const { proxyID } = useProxy();

  const { isReadonly } = useConfig();
  const { fetchDescribeOdigos } = useDescribe();
  const { tokens, updateToken } = useTokenCRUD();
  const { status, title, message } = useStatusStore();

  const onClickAccessManagement = () => router.push(ROUTES.ACCESS_MANAGEMENT);

  const left = useMemo(() => {
    const arr = [<OdigosCentralLogoText key='logo' size={150} />];

    if (title || message) {
      if (v2) {
        arr.push(<V2Badge key='status' status={status} label={message} />);
      } else {
        arr.push(<Status key='status' status={status} title={title} subtitle={message} size={14} family='primary' withIcon withBackground />);
      }
    }

    if (isReadonly) {
      if (v2) {
        arr.push(
          <Tooltip key='readonly' text={FORM_ALERTS.READONLY_WARNING}>
            <V2Badge status={OtherStatusType.Disabled} label='Read Only' />
          </Tooltip>,
        );
      } else {
        arr.push(
          <Tooltip key='readonly' text={FORM_ALERTS.READONLY_WARNING}>
            <Status status={StatusType.Info} title='Read Only' size={14} family='primary' withIcon withBackground />
          </Tooltip>,
        );
      }
    }

    arr.push(<ConnectionSelect key='connection-select' />);

    return arr;
  }, [v2, title, message, status, isReadonly]);

  const right = useMemo(() => {
    const arr = [<NotificationManager key='notification-manager' />];
    if (!v2) arr.unshift(<ToggleDarkMode key='toggle-theme' />);
    if (proxyID) arr.push(<SystemOverview key='system-overview' tokens={tokens} saveToken={updateToken} fetchDescribeOdigos={fetchDescribeOdigos} />);
    arr.push(...[<SlackInvite key='slack-invite' />, <AccountMenu key='account-menu' />]);

    return arr;
  }, [v2, proxyID, tokens, updateToken, fetchDescribeOdigos, onClickAccessManagement]);

  if (v2) {
    return <V2Header left={left} right={right} />;
  }

  return <Header left={left} right={right} />;
};

export { OverviewHeader };
