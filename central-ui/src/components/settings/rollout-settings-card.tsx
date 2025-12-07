'use client';

import React from 'react';
import styled from 'styled-components';
import { useRemoteConfig } from '@/hooks';
import Theme from '@odigos/ui-kit/theme';
import { FlexRow, FlexColumn, Text, Toggle } from '@odigos/ui-kit/components';

const Card = styled.div`
  background-color: ${({ theme }) => theme.colors.dropdown_bg};
  border: 1px solid ${({ theme }) => theme.colors.border};
  border-radius: 24px;
  padding: 24px;
  width: 100%;
  max-width: 600px;
`;

const Header = styled(FlexRow)`
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
`;

const Description = styled(Text)`
  color: ${({ theme }) => theme.text.secondary};
  margin-top: 8px;
`;

const SettingRow = styled(FlexRow)`
  justify-content: space-between;
  align-items: center;
  padding: 16px 0;
  border-top: 1px solid ${({ theme }) => theme.colors.border};
`;

export const RolloutSettingsCard: React.FC = () => {
  const theme = Theme.useTheme();
  const { isAutomaticRolloutDisabled, setAutomaticRolloutDisabled, loading } = useRemoteConfig();

  const handleToggle = async () => {
    console.log('handleToggle', isAutomaticRolloutDisabled);
    const newValue = !isAutomaticRolloutDisabled;
    await setAutomaticRolloutDisabled(newValue);
  };

  return (
    <Card>
      <Header>
        <FlexColumn>
          <Text size={18} weight={600} color={theme.text.primary}>
            Rollout Settings
          </Text>
          <Description size={14}>Control how Odigos handles workload rollouts after instrumentation changes.</Description>
        </FlexColumn>
      </Header>

      <SettingRow>
        <FlexColumn style={{ flex: 1, marginRight: 16 }}>
          <Text size={14} weight={500} color={theme.text.primary}>
            Disable Automatic Rollouts
          </Text>
          <Text size={12} color={theme.text.secondary} style={{ marginTop: 4 }}>
            When enabled, Odigos will not automatically trigger workload rollouts when instrumenting or uninstrumenting sources. You will need to manually restart workloads for changes to take effect.
          </Text>
        </FlexColumn>
        <Toggle initialValue={isAutomaticRolloutDisabled ?? false} onChange={handleToggle} disabled={loading} />
      </SettingRow>
    </Card>
  );
};
