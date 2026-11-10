import type { Role } from './auth';
import type { AuthProviderId } from './auth-providers';

export interface GroupRoleMapping {
  id: string;
  providerId: AuthProviderId;
  claimName: string;
  claimValue: string;
  role: Role;
  // Optional human-readable label shown instead of the raw claim value (GUID).
  displayName?: string | null;
}

export interface GroupRoleMappingInput {
  providerId: AuthProviderId;
  // Token claim to match on. Defaults to "groups" on the backend when omitted.
  claimName?: string;
  // The group identifier emitted by the IdP (e.g. Azure group object ID or name).
  claimValue: string;
  role: Role;
  // Optional human-readable label for the group (cosmetic only).
  displayName?: string;
}
