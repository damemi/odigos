import { ROUTES } from '../constants';
import { PlatformType, SVG } from '@odigos/ui-kit/types';
import { NavbarProps } from '@odigos/ui-kit/components';
import { AppRouterInstance } from 'next/dist/shared/lib/app-router-context.shared-runtime';
import { ConnectionsIcon, OverviewIcon, PipelineCollectorIcon, SamplingIcon, ServiceMapIcon, SettingsIcon, UserSettingsIcon } from '@odigos/ui-kit/icons';

const getPayloadForIcon = (router: AppRouterInstance, currentPath: string, targetPath: string, label: string, icon: SVG): NavbarProps['icons'][number] => {
  return {
    id: targetPath,
    label,
    icon,
    selected: currentPath === targetPath,
    onClick: () => router.push(targetPath),
  };
};

// Kubernetes keeps the full in-proxy nav. Cloud connectors add Collectors only —
// the page renders managed collectors there, and the Kubernetes pipeline view otherwise.
// VM agents stay on Overview.
export const getNavbarIcons = (router: AppRouterInstance, currentPath: string, proxyID?: string, platformType?: PlatformType) => {
  const navIcons: NavbarProps['icons'] = [];

  navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.CONNECTIONS, 'Connections', ConnectionsIcon));

  if (proxyID) {
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.OVERVIEW, 'Overview', OverviewIcon));

    if (platformType === PlatformType.K8s) {
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.SERVICE_MAP, 'Service Map', ServiceMapIcon));
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.COLLECTORS, 'Collectors', PipelineCollectorIcon));
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.SAMPLING, 'Sampling Rules', SamplingIcon));
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.SETTINGS, 'Settings', SettingsIcon));
    } else if (platformType === PlatformType.Connector) {
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.COLLECTORS, 'Collectors', PipelineCollectorIcon));
    }
  } else {
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.ACCESS_MANAGEMENT, 'Access Management', UserSettingsIcon));
  }

  return navIcons;
};
