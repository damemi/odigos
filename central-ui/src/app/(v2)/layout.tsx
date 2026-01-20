'use client';

import React, { type PropsWithChildren, useEffect } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { getNavbarIcons } from '@/utils';
import { useTheme } from 'styled-components';
import { OverviewHeader } from '@/components';
import { useDarkMode } from '@odigos/ui-kit/store';
import { Navbar } from '@odigos/ui-kit/components/v2';
import { useProxy, useSSE, useTokenTracker } from '@/hooks';
import { ErrorBoundary, FlexColumn, FlexRow } from '@odigos/ui-kit/components';

function V2Layout({ children }: PropsWithChildren) {
  // call important hooks that should run on page-mount
  useSSE();
  useTokenTracker();

  const router = useRouter();
  const pathname = usePathname();
  const { proxyID } = useProxy();

  // TODO: remove this after migration to v2
  const theme = useTheme();
  const { darkMode, setDarkMode } = useDarkMode();
  useEffect(() => {
    if (!darkMode) setDarkMode(true);
    document.body.style.backgroundColor = theme.v2.colors.black['500'];
  }, [theme]);

  return (
    <ErrorBoundary>
      <FlexColumn $gap={0}>
        <OverviewHeader v2 />
        <FlexRow $gap={0}>
          <Navbar height='calc(100vh - 90px)' icons={getNavbarIcons(router, pathname, proxyID)} />
          {children}
        </FlexRow>
      </FlexColumn>
    </ErrorBoundary>
  );
}

export default V2Layout;
