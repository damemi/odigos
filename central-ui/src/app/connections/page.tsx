'use client';

import React from 'react';
import styled from 'styled-components';
import { ConnectionsMain } from '@/components';
import { OdigosLogoText } from '@odigos/ui-kit/icons';
import { FlexColumn, Header } from '@odigos/ui-kit/components';
import { SlackInvite, ToggleDarkMode } from '@odigos/ui-kit/containers';

export const PageWrapper = styled(FlexColumn)`
  align-items: center;
`;

export default function Page() {
  return (
    <PageWrapper>
      <Header left={[<OdigosLogoText key='logo' size={100} />]} right={[<ToggleDarkMode key='toggle-theme' />, <SlackInvite key='slack-invite' />]} />
      <ConnectionsMain />
    </PageWrapper>
  );
}
