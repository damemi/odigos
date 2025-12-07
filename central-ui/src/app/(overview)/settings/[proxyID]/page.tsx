'use client';

import React from 'react';
import styled from 'styled-components';
import Theme from '@odigos/ui-kit/theme';
import { FlexColumn, Text } from '@odigos/ui-kit/components';
import { RolloutSettingsCard } from '@/components';

const Container = styled(FlexColumn)`
  width: 100%;
  padding: 24px;
  gap: 24px;
`;

const PageHeader = styled.div`
  margin-bottom: 8px;
`;

export default function SettingsPage() {
  const theme = Theme.useTheme();

  return (
    <Container>
      <PageHeader>
        <Text size={24} weight={600} color={theme.text.primary}>
          Cluster Settings
        </Text>
        <Text size={14} color={theme.text.secondary} style={{ marginTop: 8 }}>
          Configure settings for this cluster. These settings are managed remotely and persist through Helm upgrades.
        </Text>
      </PageHeader>

      <RolloutSettingsCard />
    </Container>
  );
}

