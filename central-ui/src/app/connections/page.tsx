'use client';

import React from 'react';
import styled from 'styled-components';
import { FlexColumn } from '@odigos/ui-kit/components';
import { ConnectionsMain, OverviewHeader } from '@/components';

export const PageWrapper = styled(FlexColumn)`
  align-items: center;
`;

export default function Page() {
  return (
    <PageWrapper>
      <OverviewHeader />
      <ConnectionsMain />
    </PageWrapper>
  );
}
