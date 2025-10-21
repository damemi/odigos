import React, { useMemo } from 'react';
import { useRouter } from 'next/navigation';
import { useStatusStore } from '../../store';
import { AccountMenu } from '../account-menu';
import { StatusType } from '@odigos/ui-kit/types';
import { ACCESS_MANAGEMENT, ROUTES } from '@/utils';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { ConnectionSelect } from '../connection-select';
import { OdigosLogoText, UserSettingsIcon } from '@odigos/ui-kit/icons';
import { Header, IconButton, Status, Tag, Tooltip } from '@odigos/ui-kit/components';
import { useConfig, useDescribe, useOdigosConfigCRUD, useProxyID, useTokenCRUD } from '@/hooks';
import { NotificationManager, SlackInvite, SystemOverview, SystemSettings, ToggleDarkMode } from '@odigos/ui-kit/containers';

const OverviewHeader = () => {
  const router = useRouter();
  const { proxyID } = useProxyID();

  const { fetchDescribeOdigos } = useDescribe();
  const { tokens, updateToken } = useTokenCRUD();
  const { status, title, message } = useStatusStore();
  const { isReadonly, installationMethod } = useConfig();
  const { fetchOdigosConfig, updateOdigosConfig } = useOdigosConfigCRUD();

  const onClickAccessManagement = () => router.push(ROUTES.ACCESS_MANAGEMENT);

  const left = useMemo(() => {
    const arr = [
      <OdigosLogoText key='logo' size={150} />,
      <Tag key='tower' type={StatusType.Default}>
        [tower]
      </Tag>,
      <ConnectionSelect key='connection-select' />,
    ];

    if (title || message) {
      arr.push(<Status key='status' status={status} title={title} subtitle={message} size={14} family='primary' withIcon withBackground />);
    }

    if (isReadonly) {
      arr.push(
        <Tooltip key='readonly' text={FORM_ALERTS.READONLY_WARNING}>
          <Status status={StatusType.Info} title='Read Only' size={14} family='primary' withIcon withBackground />
        </Tooltip>,
      );
    }

    return arr;
  }, [title, message, status, isReadonly]);

  const right = useMemo(() => {
    const arr = [<ToggleDarkMode key='toggle-theme' />, <NotificationManager key='notification-manager' />];

    if (proxyID) {
      arr.push(<SystemOverview key='system-overview' tokens={tokens} saveToken={updateToken} fetchDescribeOdigos={fetchDescribeOdigos} />);
      arr.push(<SystemSettings key='system-settings' installationMethod={installationMethod} fetchSettings={fetchOdigosConfig} onSave={updateOdigosConfig} />);
    }

    arr.push(
      ...[
        <IconButton key='access-management' tooltip={ACCESS_MANAGEMENT} onClick={onClickAccessManagement}>
          <UserSettingsIcon />
        </IconButton>,
        <SlackInvite key='slack-invite' />,
        <AccountMenu key='account-menu' />,
      ],
    );

    return arr;
  }, [proxyID, tokens, updateToken, fetchDescribeOdigos, installationMethod, fetchOdigosConfig, updateOdigosConfig, onClickAccessManagement]);

  return <Header left={left} right={right} />;
};

export { OverviewHeader };
