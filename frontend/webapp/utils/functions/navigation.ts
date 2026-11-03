import { ROUTES } from '../constants';
import { SVG } from '@odigos/ui-kit/types';
import { GatedFeature } from '@odigos/ui-kit/constants';
import { NavbarProps } from '@odigos/ui-kit/components';
import { AppRouterInstance } from 'next/dist/shared/lib/app-router-context.shared-runtime';
import { CodeIcon, InsightsIcon, OverviewIcon, PipelineCollectorIcon, ProfilingIcon, SamplingIcon, ServiceMapIcon, SettingsIcon, UrlTemplatizationIcon } from '@odigos/ui-kit/icons';

const getPayloadForIcon = (router: AppRouterInstance, currentPath: string, targetPath: string, label: string, icon: SVG): NavbarProps['icons'][number] => {
  return {
    id: targetPath,
    label,
    icon,
    selected: currentPath === targetPath,
    onClick: () => router.push(targetPath),
  };
};

export const getNavbarIcons = (router: AppRouterInstance, currentPath: string) => {
  const navIcons: NavbarProps['icons'] = [];
  const add = (path: string, label: string, icon: SVG, place: { group?: string; bottom?: boolean; gatedFeature?: GatedFeature } = {}) =>
    navIcons.push({ ...getPayloadForIcon(router, currentPath, path, label, icon), ...place });

  // PLAT-1874: the home page first, then by group (the menu shows their labels), Settings at the bottom.
  add(ROUTES.OVERVIEW, 'Overview', OverviewIcon);

  // Pages whose feature can be off stay listed, marked "Turned off" by the menu while it is.
  add(ROUTES.SERVICE_MAP, 'Service Map', ServiceMapIcon, { group: 'Observe', gatedFeature: GatedFeature.ServiceMap });
  // Listed even when insights are off: the page says so and how to turn them on.
  add(ROUTES.INSIGHTS, 'Insights', InsightsIcon, { group: 'Observe', gatedFeature: GatedFeature.Insights });
  add(ROUTES.PROFILING, 'Profiling', ProfilingIcon, { group: 'Observe', gatedFeature: GatedFeature.Profiling });

  add(ROUTES.COLLECTORS, 'Collectors', PipelineCollectorIcon, { group: 'Pipeline' });
  add(ROUTES.SAMPLING, 'Sampling Rules', SamplingIcon, { group: 'Pipeline' });
  add(ROUTES.URL_TEMPLATIZATION, 'URL Templatization', UrlTemplatizationIcon, { group: 'Pipeline' });
  add(ROUTES.INSTRUMENTATION_AGENTS, 'Instrumentation Agents', CodeIcon, { group: 'Pipeline' });

  add(ROUTES.SETTINGS, 'Settings', SettingsIcon, { bottom: true });

  return navIcons;
};
