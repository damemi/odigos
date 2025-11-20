import { InstallationMethod, type OdigosConfig, Tier } from '@odigos/ui-kit/types';

export enum InstallationStatus {
  New = 'NEW',
  AppsSelected = 'APPS_SELECTED',
  Finished = 'FINISHED',
}

export interface FetchedConfig {
  tier: Tier;
  readonly: boolean;
  installationMethod: InstallationMethod;
  installationStatus: InstallationStatus;
}
