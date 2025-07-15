'use client';

import React, { useCallback, useMemo, type PropsWithChildren } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { ROUTES } from '@/utils';
import styled from 'styled-components';
import { useModalStore } from '@odigos/ui-kit/store';
import { ConnectionsIcon } from '@odigos/ui-kit/icons';
import { EntityTypes, OtherEntityTypes } from '@odigos/ui-kit/types';
import { OverviewHeader, OverviewModalsAndDrawers } from '@/components';
import { useDataStreamsCRUD, useProxyID, useTokenTracker } from '@/hooks';
import { DataFlowActionsMenu, NavIconIds, SideNav } from '@odigos/ui-kit/containers';
import { ErrorBoundary, FlexColumn, FlexRow, type NavIcon } from '@odigos/ui-kit/components';

const PageContent = styled(FlexColumn)`
  width: 100%;
  height: 100vh;
  background-color: ${({ theme }) => theme.colors.primary};
  align-items: center;
`;

const ContentWithActions = styled.div`
  width: 100%;
  height: calc(100vh - 176px);
  position: relative;
`;

const ContentUnderActions = styled(FlexRow)`
  align-items: flex-start !important;
  justify-content: space-between;
  padding-left: 12px;
  width: calc(100% - 12px);
`;

const getEntityType = (pathname: string, proxyID: string) => {
  return pathname.includes(ROUTES.SOURCES(proxyID))
    ? EntityTypes.Source
    : pathname.includes(ROUTES.DESTINATIONS(proxyID))
    ? EntityTypes.Destination
    : pathname.includes(ROUTES.ACTIONS(proxyID))
    ? EntityTypes.Action
    : pathname.includes(ROUTES.INSTRUMENTATION_RULES(proxyID))
    ? EntityTypes.InstrumentationRule
    : undefined;
};

const getSelectedId = (pathname: string, proxyID: string) => {
  return pathname.includes(ROUTES.OVERVIEW(proxyID))
    ? NavIconIds.Overview
    : pathname.includes(ROUTES.SOURCES(proxyID))
    ? NavIconIds.Sources
    : pathname.includes(ROUTES.DESTINATIONS(proxyID))
    ? NavIconIds.Destinations
    : pathname.includes(ROUTES.ACTIONS(proxyID))
    ? NavIconIds.Actions
    : pathname.includes(ROUTES.INSTRUMENTATION_RULES(proxyID))
    ? NavIconIds.InstrumentationRules
    : undefined;
};

const routesMap = {
  [NavIconIds.Overview]: (proxyID: string) => ROUTES.OVERVIEW(proxyID),
  [NavIconIds.Sources]: (proxyID: string) => ROUTES.SOURCES(proxyID),
  [NavIconIds.Destinations]: (proxyID: string) => ROUTES.DESTINATIONS(proxyID),
  [NavIconIds.Actions]: (proxyID: string) => ROUTES.ACTIONS(proxyID),
  [NavIconIds.InstrumentationRules]: (proxyID: string) => ROUTES.INSTRUMENTATION_RULES(proxyID),
};

function OverviewLayout({ children }: PropsWithChildren) {
  // call important hooks that should run on page-mount
  // useSSE();
  useTokenTracker();
  const { updateDataStream, deleteDataStream } = useDataStreamsCRUD();
  const { setCurrentModal } = useModalStore();

  const router = useRouter();
  const pathname = usePathname();
  const { proxyID } = useProxyID();

  const entityType = useMemo(() => getEntityType(pathname, proxyID), [pathname, proxyID]);
  const selectedId = useMemo(() => getSelectedId(pathname, proxyID), [pathname, proxyID]);

  const onClickId = useCallback(
    (navId: NavIconIds) => {
      const route = routesMap[navId]?.(proxyID);
      if (route) router.push(route);
    },
    [proxyID, router],
  );

  const extendedNavIcons: NavIcon[] = useMemo(
    () => [
      {
        id: ROUTES.CONNECTIONS,
        icon: ConnectionsIcon,
        selected: pathname.includes(ROUTES.CONNECTIONS),
        onClick: () => router.push(ROUTES.CONNECTIONS),
      },
    ],
    [pathname, router],
  );

  return (
    <ErrorBoundary>
      <PageContent>
        <OverviewHeader />

        <ContentWithActions>
          <DataFlowActionsMenu
            addEntity={entityType}
            onClickNewDataStream={() => setCurrentModal(OtherEntityTypes.DataStream)}
            updateDataStream={updateDataStream}
            deleteDataStream={deleteDataStream}
          />
          <ContentUnderActions>
            <SideNav defaultSelectedId={selectedId} onClickId={onClickId} extendedNavIcons={extendedNavIcons} />
            {children}
          </ContentUnderActions>
        </ContentWithActions>

        <OverviewModalsAndDrawers />
      </PageContent>
    </ErrorBoundary>
  );
}

export default OverviewLayout;
