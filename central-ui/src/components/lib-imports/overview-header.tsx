import React from 'react';
import { useRouter } from 'next/navigation';
import { useStatusStore } from '../../store';
import { AccountMenu } from '../account-menu';
import { StatusType } from '@odigos/ui-kit/types';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { ConnectionSelect } from '../connection-select';
import { ACCESS_MANAGEMENT_TITLE, ROUTES } from '@/utils';
import { OdigosLogoText, UserSettingsIcon } from '@odigos/ui-kit/icons';
import { useConfig, useDescribe, useOdigosConfigCRUD, useTokenCRUD } from '@/hooks';
import { Header, IconButton, Status, Tag, Tooltip } from '@odigos/ui-kit/components';
import { NotificationManager, SlackInvite, SystemOverview, SystemSettings, ToggleDarkMode } from '@odigos/ui-kit/containers';

const OverviewHeader = () => {
  const router = useRouter();

  const { fetchDescribeOdigos } = useDescribe();
  const { tokens, updateToken } = useTokenCRUD();
  const { status, title, message } = useStatusStore();
  const { isReadonly, installationMethod } = useConfig();
  const { fetchOdigosConfig, updateOdigosConfig } = useOdigosConfigCRUD();

  const onClickHome = () => router.push(ROUTES.ROOT);
  const onClickAccessManagement = () => router.push(ROUTES.ACCESS_MANAGEMENT);

  return (
    <Header
      left={[
        <OdigosLogoText key='logo' size={100} onClick={onClickHome} />,
        <Tag key='central' type={StatusType.Default}>
          [central]
        </Tag>,
        <ConnectionSelect key='connection-select' />,
        title ? <Status key='status' status={status} title={title} subtitle={message} size={14} family='primary' withIcon withBackground /> : null,
        isReadonly && (
          <Tooltip key='readonly' text={FORM_ALERTS.READONLY_WARNING}>
            <Status status={StatusType.Info} title='Read Only' size={14} family='primary' withIcon withBackground />
          </Tooltip>
        ),
      ]}
      right={[
        <ToggleDarkMode key='toggle-theme' />,
        <NotificationManager key='notification-manager' />,
        <SystemOverview key='system-overview' tokens={tokens} saveToken={updateToken} fetchDescribeOdigos={fetchDescribeOdigos} />,
        <SystemSettings key='system-settings' installationMethod={installationMethod} fetchSettings={fetchOdigosConfig} onSave={updateOdigosConfig} />,
        <IconButton key='access-management' tooltip={ACCESS_MANAGEMENT_TITLE} onClick={onClickAccessManagement}>
          <UserSettingsIcon />
        </IconButton>,
        <SlackInvite key='slack-invite' />,
        <AccountMenu key='account-menu' />,
      ]}
    />
  );
};

export { OverviewHeader };
