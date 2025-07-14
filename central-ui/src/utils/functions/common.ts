import { Role } from '@/types';
import { StatusType } from '@odigos/ui-kit/types';

export const getStatusForRole = (role: Role): StatusType => {
  switch (role) {
    case Role.VIEWER:
      return StatusType.Info;
    case Role.ADMIN:
      return StatusType.Error;
    case Role.MAINTAINER:
      return StatusType.Warning;
    case Role.OWNER:
      return StatusType.Default;
  }
};
