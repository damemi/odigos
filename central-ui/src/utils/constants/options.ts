import { Role } from '@/types';
import type { DropDataOption } from '@odigos/ui-kit/components/v2';

export const ROLE_OPTIONS: DropDataOption[] = [
  { id: Role.ADMIN, label: Role.ADMIN },
  { id: Role.MAINTAINER, label: Role.MAINTAINER },
  { id: Role.VIEWER, label: Role.VIEWER },
];
