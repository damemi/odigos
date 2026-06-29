import { Role } from '@/types';
import type { DropDataOption } from '@odigos/ui-kit/components/v2';

// Human-friendly Title Case labels for roles. The `Role` enum values stay
// uppercase (backend contract); this map is the single source of truth for how
// a role is *displayed* (dropdown options, table badge, etc.).
export const ROLE_LABELS: Record<Role, string> = {
  [Role.OWNER]: 'Owner',
  [Role.ADMIN]: 'Admin',
  [Role.MAINTAINER]: 'Maintainer',
  [Role.VIEWER]: 'Viewer',
};

export const ROLE_OPTIONS: DropDataOption[] = [
  { id: Role.ADMIN, label: ROLE_LABELS[Role.ADMIN] },
  { id: Role.MAINTAINER, label: ROLE_LABELS[Role.MAINTAINER] },
  { id: Role.VIEWER, label: ROLE_LABELS[Role.VIEWER] },
];
