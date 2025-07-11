import React from 'react';
import { useStatusStore } from '../../store';
import { useRouter } from 'next/navigation';
import { StatusType } from '@odigos/ui-kit/types';
import { FORM_ALERTS } from '@odigos/ui-kit/constants';
import { ACCESS_MANAGEMENT_TITLE, ROUTES } from '@/utils';
import { ListIcon, OdigosLogoText } from '@odigos/ui-kit/icons';
import { Header, IconButton, Status, Tooltip } from '@odigos/ui-kit/components';
import { useConfig, useConnectionsCRUD, useDescribe, useProxyID, useTokenCRUD } from '@/hooks';
import { ComputePlatformSelect, NotificationManager, SlackInvite, SystemOverview, ToggleDarkMode } from '@odigos/ui-kit/containers';

const OverviewHeader = () => {
  const { isReadonly } = useConfig();
  const { fetchDescribeOdigos } = useDescribe();
  const { tokens, updateToken } = useTokenCRUD();
  const { status, title, message } = useStatusStore();
  const { proxyID, goSelectNewProxy, selectProxyID } = useProxyID();
  const { connections, currentConnection } = useConnectionsCRUD(proxyID);

  const router = useRouter();

  const handleAccessManagementClick = () => {
    router.push(ROUTES.ACCESS_MANAGEMENT);
  };

  return (
    <Header
      left={[
        <OdigosLogoText key='logo' size={100} onClick={() => router.push(ROUTES.ROOT)} />,
        <ComputePlatformSelect key='cp-select' connections={connections} selected={currentConnection} onSelect={({ id }) => selectProxyID(id)} onViewAll={() => goSelectNewProxy()} />,
        title ? <Status key='status' status={status} title={title} subtitle={message} size={14} family='primary' withIcon withBackground /> : null,
        isReadonly && (
          <Tooltip key='readonly' text={FORM_ALERTS.READONLY_WARNING}>
            <Status status={StatusType.Info} title='Read Only' size={14} family='primary' withIcon withBackground />
          </Tooltip>
        ),
      ]}
      right={[
        <ToggleDarkMode key='toggle-theme' />,
        <IconButton key='cli' onClick={handleAccessManagementClick} tooltip={ACCESS_MANAGEMENT_TITLE}>
          <ListIcon key='list-icon' />
        </IconButton>,
        <NotificationManager key='notifs' />,
        <SystemOverview key='cli' tokens={tokens} saveToken={updateToken} fetchDescribeOdigos={fetchDescribeOdigos} />,
        <SlackInvite key='slack-invite' />,
      ]}
    />
  );
};

export { OverviewHeader };
