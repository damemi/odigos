import { StatusType } from '@odigos/ui-kit/types';

export interface FetchedConnection {
  proxyID: string;
  name: string;
  status: StatusType;
}
