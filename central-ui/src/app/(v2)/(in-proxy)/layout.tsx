'use client';

import React, { type PropsWithChildren } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import styled from 'styled-components';
import { getNavbarIcons } from '@/utils';
import { OverviewHeader } from '@/components';
import { Navbar } from '@odigos/ui-kit/components';
import { OdigosProvider } from '@odigos/ui-kit/contexts';
import { PlatformType, Tier } from '@odigos/ui-kit/types';
import { useProxy, useSSE, useTokenTracker } from '@/hooks';
import { ErrorBoundary, FlexColumn, FlexRow } from '@odigos/ui-kit/components';

// this is needed in the returned JSX to inherit values from odigos provider and not lose them in the root of the layout
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
  const { proxyID, platformType, odigosVersion } = useProxy();

  return (
    <ErrorBoundary>
      <OdigosProvider platformType={platformType} tier={Tier.Onprem} version={odigosVersion || ''}>
        <Listeners />
        <ViewportColumn $gap={0}>
          <OverviewHeader />
          <ContentRow $gap={0}>
            <Navbar height='calc(100vh - 60px)' icons={getNavbarIcons(router, pathname, proxyID, platformType !== PlatformType.K8s)} />
            {children}
          </ContentRow>
        </ViewportColumn>
      </OdigosProvider>
    </ErrorBoundary>
  );
}

export default Layout;
