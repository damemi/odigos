'use client';

import React from 'react';
import styled from 'styled-components';
import { FlexColumn } from '@odigos/ui-kit/components';
import { ConnectionsActionsMenu, ConnectionsTable, OverviewHeader } from '@/components';

const Padding = styled.div`
  padding: 24px;
`;

export default function Page() {
  return (
    <FlexColumn>
      <OverviewHeader />
      <Padding>
        <ConnectionsActionsMenu />
        <ConnectionsTable />
      </Padding>
    </FlexColumn>
  );
}
