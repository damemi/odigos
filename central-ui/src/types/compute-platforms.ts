import type { Team, User } from './auth';
import { PlatformType, StatusType } from '@odigos/ui-kit/types';

export interface ComputePlatform {
  id: string;
  name: string;
  type: PlatformType;
  status: StatusType;
  users?: User[];
  teams?: Team[];
}

export interface ComputePlatformInput {
  addUserIds: string[];
  removeUserIds: string[];
  addTeamIds: string[];
  removeTeamIds: string[];
}
