'use client';

import React, { type PropsWithChildren } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import styled from 'styled-components';
import { getNavbarIcons } from '@/utils';
import { OverviewHeader } from '@/components';
import { OdigosProvider } from '@odigos/ui-kit/contexts';
import { PlatformType, Tier } from '@odigos/ui-kit/types';
import { useProxy, useSSE, useTokenTracker } from '@/hooks';
import { ErrorBoundary, FlexColumn, IconsNav } from '@odigos/ui-kit/components';

const PageContent = styled(FlexColumn)`
  width: 100%;
  height: 100vh;
  background-color: ${({ theme }) => theme.colors.primary};
  align-items: center;
`;

const Content = styled.div`
  display: flex;
  gap: 12px;
  padding: 12px;
  width: calc(100% - 24px);
  height: 100%;
`;

function Layout({ children }: PropsWithChildren) {
  // call important hooks that should run on page-mount
  useSSE();
  useTokenTracker();

  const router = useRouter();
  const pathname = usePathname();
  const { proxyID, platformType, odigosVersion } = useProxy();

  return (
    <ErrorBoundary>
      <OdigosProvider platformType={platformType} tier={Tier.Onprem} version={odigosVersion || ''}>
        <PageContent>
          <OverviewHeader />
          <Content>
            <IconsNav orientation='vertical' mainIcons={getNavbarIcons(router, pathname, proxyID, platformType === PlatformType.Vm)} subIcons={[]} />
            <div style={{ width: '100%' }}>{children}</div>
          </Content>
        </PageContent>
      </OdigosProvider>
    </ErrorBoundary>
  );
}

export default Layout;
