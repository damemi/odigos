import { Role } from '@/types';
import type { DropDataOption } from '@odigos/ui-kit/components';

// Human-friendly Title Case labels for roles. The `Role` enum values stay
// uppercase (backend contract); this map is the single source of truth for how
// a role is *displayed* (dropdown options, table badge, etc.).
export const ROLE_LABELS: Record<Role, string> = {
  [Role.OWNER]: 'Owner',
  [Role.ADMIN]: 'Admin',
  [Role.MAINTAINER]: 'Maintainer',
  [Role.VIEWER]: 'Viewer',
  [Role.NO_ACCESS]: 'No Access',
};

export const ROLE_OPTIONS: DropDataOption[] = [
  { id: Role.ADMIN, label: ROLE_LABELS[Role.ADMIN] },
  { id: Role.MAINTAINER, label: ROLE_LABELS[Role.MAINTAINER] },
  { id: Role.VIEWER, label: ROLE_LABELS[Role.VIEWER] },
];

export const EDIT_USER_ROLE_OPTIONS: DropDataOption[] = [...ROLE_OPTIONS, { id: Role.NO_ACCESS, label: ROLE_LABELS[Role.NO_ACCESS] }];
