import React from 'react';
import { useRouter } from 'next/navigation';
import { useStatusStore } from '../../store';
import { AccountMenu } from '../account-menu';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { ACCESS_MANAGEMENT_TITLE, ROUTES } from '@/utils';
import { Connection, StatusType } from '@odigos/ui-kit/types';
import { OdigosLogoText, UserSettingsIcon } from '@odigos/ui-kit/icons';
import { Header, IconButton, Status, Tag, Tooltip } from '@odigos/ui-kit/components';
import { useConfig, useConnectionsCRUD, useDescribe, useOdigosConfigCRUD, useProxyID, useTokenCRUD } from '@/hooks';
import { ComputePlatformSelect, NotificationManager, SlackInvite, SystemOverview, SystemSettings, ToggleDarkMode } from '@odigos/ui-kit/containers';

const OverviewHeader = () => {
  const router = useRouter();

  const { fetchDescribeOdigos } = useDescribe();
  const { tokens, updateToken } = useTokenCRUD();
  const { status, title, message } = useStatusStore();
  const { isReadonly, installationMethod } = useConfig();
  const { fetchOdigosConfig, updateOdigosConfig } = useOdigosConfigCRUD();

  const { proxyID, goSelectNewProxy, selectProxyID } = useProxyID();
  const { connections, currentConnection } = useConnectionsCRUD(proxyID);

  const onClickHome = () => router.push(ROUTES.ROOT);
  const onClickPlatform = ({ id }: Connection) => selectProxyID(id);
  const onClickAllPlatforms = () => goSelectNewProxy();
  const onClickAccessManagement = () => router.push(ROUTES.ACCESS_MANAGEMENT);

  return (
    <Header
      left={[
        <OdigosLogoText key='logo' size={100} onClick={onClickHome} />,
        <Tag key='central' type={StatusType.Default}>
          [central]
        </Tag>,
        currentConnection ? <ComputePlatformSelect key='cp-select' connections={connections} selected={currentConnection} onSelect={onClickPlatform} onViewAll={onClickAllPlatforms} /> : null,
        title ? <Status key='status' status={status} title={title} subtitle={message} size={14} family='primary' withIcon withBackground /> : null,
        isReadonly && (
          <Tooltip key='readonly' text={FORM_ALERTS.READONLY_WARNING}>
            <Status status={StatusType.Info} title='Read Only' size={14} family='primary' withIcon withBackground />
          </Tooltip>
        ),
      ]}
      right={[
        <ToggleDarkMode key='toggle-theme' />,
        <IconButton key='access-management' tooltip={ACCESS_MANAGEMENT_TITLE} onClick={onClickAccessManagement}>
          <UserSettingsIcon />
        </IconButton>,
        <NotificationManager key='notification-manager' />,
        <SystemOverview key='system-overview' tokens={tokens} saveToken={updateToken} fetchDescribeOdigos={fetchDescribeOdigos} />,
        <SystemSettings key='system-settings' installationMethod={installationMethod} fetchSettings={fetchOdigosConfig} onSave={updateOdigosConfig} />,
        <SlackInvite key='slack-invite' />,
        <AccountMenu key='account-menu' />,
      ]}
    />
  );
};

export { OverviewHeader };
