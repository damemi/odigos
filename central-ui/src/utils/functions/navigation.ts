import { ROUTES } from '../constants';
import { PlatformType, SVG } from '@odigos/ui-kit/types';
import { NavbarProps } from '@odigos/ui-kit/components';
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

// Kubernetes lists every page Odigos UI has except Insights, which Central doesn't serve, so pages gated off in the
// cluster still show why; a gated item (`gatedFeature`) is marked "Turned off" while its feature is. Cloud connectors
// add Collectors only —
// the page renders managed collectors there, and the Kubernetes pipeline view otherwise.
// VM agents stay on Overview. `NavbarNext` lists items under their group's label, with Settings at the bottom.
export const getNavbarIcons = (router: AppRouterInstance, currentPath: string, proxyID?: string, platformType?: PlatformType) => {
  const navIcons: NavbarProps['icons'] = [];
  const add = (path: string, label: string, icon: SVG, place: { group?: string; bottom?: boolean; gatedFeature?: GatedFeature } = {}) =>
    navIcons.push({ ...getPayloadForIcon(router, currentPath, path, label, icon), ...place });

  add(ROUTES.CONNECTIONS, 'Connections', ConnectionsIcon);

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
    add(ROUTES.ACCESS_MANAGEMENT, 'Access Management', UserSettingsIcon);
  }

  return navIcons;
};
