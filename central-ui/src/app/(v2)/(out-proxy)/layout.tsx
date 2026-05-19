'use client';

import React, { type PropsWithChildren } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import styled from 'styled-components';
import { getNavbarIcons } from '@/utils';
import { OverviewHeader } from '@/components';
import { Navbar } from '@odigos/ui-kit/components/v2';
import { ErrorBoundary, FlexColumn, FlexRow } from '@odigos/ui-kit/components';

const ViewportColumn = styled(FlexColumn)`
  height: 100vh;
  overflow: hidden;
`;

const ContentRow = styled(FlexRow)`
  flex: 1;
  min-height: 0;
`;

function Layout({ children }: PropsWithChildren) {
  const router = useRouter();
  const pathname = usePathname();

  return (
    <ErrorBoundary>
      <ViewportColumn $gap={0}>
        <OverviewHeader />
        <ContentRow $gap={0}>
          <Navbar height='calc(100vh - 60px)' icons={getNavbarIcons(router, pathname)} />
          {children}
        </ContentRow>
      </ViewportColumn>
    </ErrorBoundary>
  );
}

export default Layout;
