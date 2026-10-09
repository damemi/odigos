'use client';

import React, { type PropsWithChildren } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import styled from 'styled-components';
import { getNavbarIcons } from '@/utils';
import { OverviewHeader } from '@/components';
import { Navbar } from '@odigos/ui-kit/components';
import { useProxy, useSSE, useTokenTracker } from '@/hooks';
import { ErrorBoundary, FlexColumn, FlexRow } from '@odigos/ui-kit/components';

// Kept in the returned JSX so it renders under `<OdigosProvider>` (now mounted by the
// shared `(v2)` layout) and inherits its values, instead of running at the root.
const Listeners = () => {
  useSSE();
  useTokenTracker();

  return null;
};

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
  const { proxyID, platformType } = useProxy();

  return (
    <ErrorBoundary>
      <Listeners />
      <ViewportColumn $gap={0}>
        <OverviewHeader />
        <ContentRow $gap={0}>
          <Navbar height='calc(100vh - 60px)' icons={getNavbarIcons(router, pathname, proxyID, platformType)} />
          {children}
        </ContentRow>
      </ViewportColumn>
    </ErrorBoundary>
  );
}

export default Layout;
