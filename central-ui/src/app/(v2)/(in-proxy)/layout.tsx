'use client';

import React, { type PropsWithChildren } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { getNavbarIcons } from '@/utils';
import { OverviewHeader } from '@/components';
import { Navbar } from '@odigos/ui-kit/components/v2';
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

function Layout({ children }: PropsWithChildren) {
  const router = useRouter();
  const pathname = usePathname();
  const { proxyID, platformType, odigosVersion } = useProxy();

  return (
    <ErrorBoundary>
      <OdigosProvider platformType={platformType} tier={Tier.Onprem} version={odigosVersion || ''}>
        <Listeners />
        <FlexColumn $gap={0}>
          <OverviewHeader v2 />
          <FlexRow $gap={0}>
            <Navbar height='calc(100vh - 90px)' icons={getNavbarIcons(router, pathname, proxyID, platformType === PlatformType.Vm)} />
            {children}
          </FlexRow>
        </FlexColumn>
      </OdigosProvider>
    </ErrorBoundary>
  );
}

export default Layout;
