import { ROUTES } from '../constants';
import { SVG } from '@odigos/ui-kit/types';
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

export const getNavbarIcons = (router: AppRouterInstance, currentPath: string, proxyID?: string, isVm?: boolean) => {
  const navIcons: NavbarProps['icons'] = [];

  navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.CONNECTIONS, 'Connections', ConnectionsIcon));

  if (proxyID) {
    navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.OVERVIEW, 'Overview', OverviewIcon));

    if (!isVm) {
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.SERVICE_MAP, 'Service Map', ServiceMapIcon));
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.PIPELINE_COLLECTORS, 'Collectors Pipeline', PipelineCollectorIcon));
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.SAMPLING, 'Sampling Rules', SamplingIcon));
      navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.SETTINGS, 'Settings', SettingsIcon));
    }
  }

  navIcons.push(getPayloadForIcon(router, currentPath, ROUTES.ACCESS_MANAGEMENT, 'Access Management', UserSettingsIcon));

  return navIcons;
};
