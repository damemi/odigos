'use client';

import React, { useCallback, useMemo, type PropsWithChildren, CSSProperties } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import styled from 'styled-components';
import { useModalStore } from '@odigos/ui-kit/store';
import { ServiceMapIcon } from '@odigos/ui-kit/icons';
import { DATA_FLOW_HEIGHT, MENU_BAR_HEIGHT, ROUTES } from '@/utils';
import { EntityTypes, OtherEntityTypes } from '@odigos/ui-kit/types';
import { OverviewHeader, OverviewModalsAndDrawers } from '@/components';
import { useDataStreamsCRUD, useProxyID, useSSE, useTokenTracker } from '@/hooks';
import { ErrorBoundary, FlexColumn, type NavIcon } from '@odigos/ui-kit/components';
import { DataFlowActionsMenu, NavIconIds, SideNav } from '@odigos/ui-kit/containers';

enum OtherNavIconIds {
  ServiceMap = 'service-map',
}

const PageContent = styled(FlexColumn)`
  width: 100%;
  height: 100vh;
  background-color: ${({ theme }) => theme.colors.primary};
  align-items: center;
`;

const ContentWithActions = styled.div<{ $height: CSSProperties['height'] }>`
  width: 100%;
  height: ${({ $height }) => $height};
  position: relative;
`;

const ContentUnderActions = styled.div`
  gap: 12px;
  display: flex;
  justify-content: space-between;
  padding: 0 12px;
  width: calc(100% - 24px);
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
    : undefined;
};

const routesMap = {
  [NavIconIds.Overview]: (proxyID: string) => ROUTES.OVERVIEW(proxyID),
  [NavIconIds.Sources]: (proxyID: string) => ROUTES.SOURCES(proxyID),
  [NavIconIds.Destinations]: (proxyID: string) => ROUTES.DESTINATIONS(proxyID),
  [NavIconIds.Actions]: (proxyID: string) => ROUTES.ACTIONS(proxyID),
  [NavIconIds.InstrumentationRules]: (proxyID: string) => ROUTES.INSTRUMENTATION_RULES(proxyID),
  [OtherNavIconIds.ServiceMap]: (proxyID: string) => ROUTES.SERVICE_MAP(proxyID),
};

function OverviewLayout({ children }: PropsWithChildren) {
  // call important hooks that should run on page-mount
  useSSE();
  useTokenTracker();

  const { setCurrentModal } = useModalStore();
  const { updateDataStream, deleteDataStream } = useDataStreamsCRUD();

  const router = useRouter();
  const pathname = usePathname();
  const { proxyID } = useProxyID();

  const entityType = useMemo(() => getEntityType(pathname, proxyID), [pathname, proxyID]);
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

        <ContentWithActions $height={DATA_FLOW_HEIGHT}>
          {selectedId !== OtherNavIconIds.ServiceMap ? (
            <DataFlowActionsMenu
              addEntity={entityType}
              onClickNewDataStream={() => setCurrentModal(OtherEntityTypes.DataStream)}
              updateDataStream={updateDataStream}
              deleteDataStream={deleteDataStream}
            />
          ) : (
            <div style={{ height: `${MENU_BAR_HEIGHT}px` }} />
          )}
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
