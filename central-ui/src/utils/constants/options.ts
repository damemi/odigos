import { Role } from '@/types';
import type { DropdownOption } from '@odigos/ui-kit/components';

export const ROLE_OPTIONS: DropdownOption<Role>[] = [
  { id: Role.OWNER, value: Role.OWNER },
  { id: Role.ADMIN, value: Role.ADMIN },
  { id: Role.MAINTAINER, value: Role.MAINTAINER },
  { id: Role.VIEWER, value: Role.VIEWER },
];
