import type { Team, User } from './auth';
import type { Connection } from '@odigos/ui-kit/types';

export interface ComputePlatform extends Connection {
  users?: User[];
  teams?: Team[];
}

export interface ComputePlatformInput {
  addUserIds: string[];
  removeUserIds: string[];
  addTeamIds: string[];
  removeTeamIds: string[];
}
