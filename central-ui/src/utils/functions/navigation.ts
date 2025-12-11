import { ROUTES } from '../constants';
import { SVG } from '@odigos/ui-kit/types';
import { NavbarProps } from '@odigos/ui-kit/components/v2';
import { AppRouterInstance } from 'next/dist/shared/lib/app-router-context.shared-runtime';
import { ActionIcon, ConnectionsIcon, DestinationIcon, InstrumentationRuleIcon, OverviewIcon, ServiceMapIcon, SourceIcon, UserSettingsIcon } from '@odigos/ui-kit/icons';

const getPayloadForIcon = (router: AppRouterInstance, currentPath: string, targetPath: string, icon: SVG): NavbarProps['icons'][number] => {
  return {
    id: targetPath,
    icon,
    selected: currentPath === targetPath,
    onClick: () => router.push(targetPath),
  };
};

export const getNavbarIcons = (router: AppRouterInstance, currentPath: string, proxyID?: string) => {
  const navIcons: NavbarProps['icons'] = [];

  navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.ACCESS_MANAGEMENT, UserSettingsIcon));
  navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.CONNECTIONS, ConnectionsIcon));

  if (proxyID) {
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.OVERVIEW(proxyID), OverviewIcon));
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.SOURCES(proxyID), SourceIcon));
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.DESTINATIONS(proxyID), DestinationIcon));
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.ACTIONS(proxyID), ActionIcon));
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.INSTRUMENTATION_RULES(proxyID), InstrumentationRuleIcon));
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.SERVICE_MAP(proxyID), ServiceMapIcon));
  }

  return navIcons;
};
