import type { Team, User } from './auth';
import type { Connection } from '@odigos/ui-kit/types';

export interface ComputePlatform extends Connection {
  // agents-api GraphQL schema version reported by VM proxies; null/undefined for k8s.
  // Used to pick the agents-api dialect for VM proxies (see odigos-api-adapter).
  graphqlSchemaVersion?: string | null;
  users?: User[];
  teams?: Team[];
}

export interface ComputePlatformInput {
  addUserIds: string[];
  removeUserIds: string[];
  addTeamIds: string[];
  removeTeamIds: string[];
}
