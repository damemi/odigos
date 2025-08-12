import { PlatformType, StatusType } from '@odigos/ui-kit/types';

export interface FetchedConnection {
  proxyID: string;
  name: string;
  status: StatusType;
}

export interface Connection {
  id: string;
  type: PlatformType;
  name: string;
  status: StatusType;
}
