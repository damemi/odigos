import type { Team, User } from './auth';
import { PlatformType, StatusType } from '@odigos/ui-kit/types';

export interface ComputePlatform {
  id: string;
  name: string;
  odigosVersion: string;
  type: PlatformType;
  status: StatusType;
  connectedAt?: number | null; // unix seconds
  lastSeenAt?: number | null; // unix seconds
  users?: User[];
  teams?: Team[];
}

export interface ComputePlatformInput {
  addUserIds: string[];
  removeUserIds: string[];
  addTeamIds: string[];
  removeTeamIds: string[];
}
