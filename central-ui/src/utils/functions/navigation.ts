import { ROUTES } from '../constants';
import { PlatformType, SVG } from '@odigos/ui-kit/types';
import type { NavbarContext, NavbarProps } from '@odigos/ui-kit/components';
import { getPlatformIcon, getPlatformLabel } from '@odigos/ui-kit/functions';
import { GatedFeature } from '@odigos/ui-kit/constants';
import { AppRouterInstance } from 'next/dist/shared/lib/app-router-context.shared-runtime';
import {
  CodeIcon,
  ConnectionsIcon,
  OverviewIcon,
  PipelineCollectorIcon,
  ProfilingIcon,
  SamplingIcon,
  ServiceMapIcon,
  SettingsIcon,
  UrlTemplatizationIcon,
  UserSettingsIcon,
} from '@odigos/ui-kit/icons';

const getPayloadForIcon = (router: AppRouterInstance, currentPath: string, targetPath: string, label: string, icon: SVG): NavbarProps['icons'][number] => {
  return {
    id: targetPath,
    label,
    icon,
    selected: currentPath === targetPath,
    onClick: () => router.push(targetPath),
  };
};

// Outside a connection: Connections and Access Management. Inside one, only its pages; the menu's context header
// (`getNavbarContext`) names the connection and leads back to all of them.
// Kubernetes lists every page Odigos UI has except Insights, which Central doesn't serve, so pages gated off in the
// cluster still show why; a gated item (`gatedFeature`) is marked "Turned off" while its feature is. Cloud connectors
// add Collectors only — the page renders managed collectors there, and the Kubernetes pipeline view otherwise.
// VM agents stay on Overview. `NavbarNext` lists items under their group's label, with Settings at the bottom.
export const getNavbarIcons = (router: AppRouterInstance, currentPath: string, proxyID?: string, platformType?: PlatformType) => {
  const navIcons: NavbarProps['icons'] = [];
  const add = (path: string, label: string, icon: SVG, place: { group?: string; bottom?: boolean; gatedFeature?: GatedFeature } = {}) =>
    navIcons.push({ ...getPayloadForIcon(router, currentPath, path, label, icon), ...place });

  if (proxyID) {
    add(ROUTES.OVERVIEW, 'Overview', OverviewIcon);

    if (platformType === PlatformType.K8s) {
      add(ROUTES.SERVICE_MAP, 'Service Map', ServiceMapIcon, { group: 'Observe', gatedFeature: GatedFeature.ServiceMap });
      add(ROUTES.PROFILING, 'Profiling', ProfilingIcon, { group: 'Observe', gatedFeature: GatedFeature.Profiling });
      add(ROUTES.COLLECTORS, 'Collectors', PipelineCollectorIcon, { group: 'Pipeline' });
      add(ROUTES.SAMPLING, 'Sampling Rules', SamplingIcon, { group: 'Pipeline' });
      add(ROUTES.URL_TEMPLATIZATION, 'URL Templatization', UrlTemplatizationIcon, { group: 'Pipeline' });
      add(ROUTES.INSTRUMENTATION_AGENTS, 'Instrumentation Agents', CodeIcon, { group: 'Pipeline' });
      add(ROUTES.SETTINGS, 'Settings', SettingsIcon, { bottom: true });
    } else if (platformType === PlatformType.Connector) {
      add(ROUTES.COLLECTORS, 'Collectors', PipelineCollectorIcon, { group: 'Pipeline' });
    }
  } else {
    add(ROUTES.CONNECTIONS, 'Connections', ConnectionsIcon);
    add(ROUTES.ACCESS_MANAGEMENT, 'Access Management', UserSettingsIcon);
  }

  return navIcons;
};

/** The open connection, heading the menu: its name and kind, and the way back to all connections. */
export const getNavbarContext = (router: AppRouterInstance, proxyName?: string, platformType?: PlatformType): NavbarContext | undefined =>
  proxyName
    ? {
        label: proxyName,
        description: getPlatformLabel(platformType),
        icon: getPlatformIcon(platformType),
        backLabel: 'All connections',
        onBack: () => router.push(ROUTES.CONNECTIONS),
      }
    : undefined;
