'use client';

import React, { useCallback, useMemo, type PropsWithChildren } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { ROUTES } from '@/utils';
import styled from 'styled-components';
import { OverviewHeader } from '@/components';
import { useProxyID, useSSE, useTokenTracker } from '@/hooks';
import { NavIconIds, SideNav } from '@odigos/ui-kit/containers';
import { ErrorBoundary, FlexColumn, type NavIcon } from '@odigos/ui-kit/components';
import { ConnectionsIcon, ServiceMapIcon, UserSettingsIcon } from '@odigos/ui-kit/icons';

enum OtherNavIconIds {
  Connections = 'connections',
  AccessManagement = 'access-management',
  ServiceMap = 'service-map',
}

const PageContent = styled(FlexColumn)`
  width: 100%;
  height: 100vh;
  background-color: ${({ theme }) => theme.colors.primary};
  align-items: center;
`;

const Content = styled.div`
  display: flex;
  gap: 12px;
  padding: 12px;
  width: calc(100% - 24px);
  height: 100%;
`;

const getSelectedId = (pathname: string, proxyID: string) => {
  return pathname === ROUTES.OVERVIEW(proxyID)
    ? NavIconIds.Overview
    : pathname === ROUTES.SOURCES(proxyID)
    ? NavIconIds.Sources
    : pathname === ROUTES.DESTINATIONS(proxyID)
    ? NavIconIds.Destinations
    : pathname === ROUTES.ACTIONS(proxyID)
    ? NavIconIds.Actions
    : pathname === ROUTES.INSTRUMENTATION_RULES(proxyID)
    ? NavIconIds.InstrumentationRules
    : pathname === ROUTES.SERVICE_MAP(proxyID)
    ? OtherNavIconIds.ServiceMap
    : pathname === ROUTES.ACCESS_MANAGEMENT
    ? OtherNavIconIds.AccessManagement
    : pathname === ROUTES.CONNECTIONS
    ? OtherNavIconIds.Connections
    : undefined;
};

const routesMap = {
  [NavIconIds.Overview]: (proxyID: string) => ROUTES.OVERVIEW(proxyID),
  [NavIconIds.Sources]: (proxyID: string) => ROUTES.SOURCES(proxyID),
  [NavIconIds.Destinations]: (proxyID: string) => ROUTES.DESTINATIONS(proxyID),
  [NavIconIds.Actions]: (proxyID: string) => ROUTES.ACTIONS(proxyID),
  [NavIconIds.InstrumentationRules]: (proxyID: string) => ROUTES.INSTRUMENTATION_RULES(proxyID),
  [OtherNavIconIds.Connections]: ROUTES.CONNECTIONS,
  [OtherNavIconIds.AccessManagement]: ROUTES.ACCESS_MANAGEMENT,
  [OtherNavIconIds.ServiceMap]: (proxyID: string) => ROUTES.SERVICE_MAP(proxyID),
};

function Layout({ children }: PropsWithChildren) {
  // call important hooks that should run on page-mount
  useSSE();
  useTokenTracker();

  const router = useRouter();
  const pathname = usePathname();
  const { proxyID } = useProxyID();
  const selectedId = useMemo(() => getSelectedId(pathname, proxyID), [pathname, proxyID]);

  const onClickId = useCallback(
    (navId: NavIconIds | OtherNavIconIds) => {
      const temp = routesMap[navId];
      const path = typeof temp === 'function' ? temp(proxyID) : temp;
      if (path) router.push(path);
    },
    [proxyID, router],
  );

  const extendedNavIcons: NavIcon[] = useMemo(
    () => [
      {
        id: OtherNavIconIds.Connections,
        icon: ConnectionsIcon,
        selected: selectedId === OtherNavIconIds.Connections,
        onClick: () => onClickId(OtherNavIconIds.Connections),
        tooltip: 'Connections',
      },
      {
        id: OtherNavIconIds.AccessManagement,
        icon: UserSettingsIcon,
        selected: selectedId === OtherNavIconIds.AccessManagement,
        onClick: () => onClickId(OtherNavIconIds.AccessManagement),
        tooltip: 'Access Management',
      },
      {
        id: OtherNavIconIds.ServiceMap,
        icon: ServiceMapIcon,
        selected: selectedId === OtherNavIconIds.ServiceMap,
        onClick: () => onClickId(OtherNavIconIds.ServiceMap),
        tooltip: 'Service Map',
      },
    ],
    [onClickId, selectedId],
  );

  return (
    <ErrorBoundary>
      <PageContent>
        <OverviewHeader />
        <Content>
          <SideNav defaultSelectedId={selectedId} onClickId={onClickId} extendedNavIcons={extendedNavIcons} />
          <div style={{ width: '100%' }}>{children}</div>
        </Content>
      </PageContent>
    </ErrorBoundary>
  );
}

export default Layout;
