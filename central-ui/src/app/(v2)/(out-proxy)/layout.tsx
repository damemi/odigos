'use client';

import React, { type PropsWithChildren } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { getNavbarIcons } from '@/utils';
import { OverviewHeader } from '@/components';
import { Navbar } from '@odigos/ui-kit/components/v2';
import { ErrorBoundary, FlexColumn, FlexRow } from '@odigos/ui-kit/components';

function Layout({ children }: PropsWithChildren) {
  const router = useRouter();
  const pathname = usePathname();

  return (
    <ErrorBoundary>
      <FlexColumn $gap={0}>
        <OverviewHeader v2 />
        <FlexRow $gap={0}>
          <Navbar height='calc(100vh - 90px)' icons={getNavbarIcons(router, pathname)} />
          {children}
        </FlexRow>
      </FlexColumn>
    </ErrorBoundary>
  );
}

export default Layout;
