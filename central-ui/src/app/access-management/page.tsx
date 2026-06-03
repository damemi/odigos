'use client';

import React, { useState } from 'react';
import { useUserStore } from '@/store';
import { PlusIcon } from '@odigos/ui-kit/icons';
import { FlexRow } from '@odigos/ui-kit/components';
import styled, { useTheme } from 'styled-components';
import { UsersTable, CreateUserModal } from '@/components';
import { Button, Segment, type SegmentProps, SegmentVariant } from '@odigos/ui-kit/components/v2';

const Container = styled.div`
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 24px;
  height: stretch;
`;

const PushRight = styled.div`
  margin-left: auto;
`;

enum AccessTab {
  Users = 'Users',
  // Teams = 'Teams',
  // Compute = 'Compute Platforms',
}

const renderActiveTab = ({ activeTab }: { activeTab: AccessTab }) => {
  const tabFactories: Record<AccessTab, () => React.ReactNode> = {
    [AccessTab.Users]: () => <UsersTable />,
    // [AccessTab.Teams]: () => <TeamsTable />,
    // [AccessTab.Compute]: () => <ComputePlatformsTable />,
  };

  return (tabFactories[activeTab] || tabFactories[AccessTab.Users])();
};

export default function Page() {
  const theme = useTheme();
  const { isAdmin } = useUserStore();
  const [activeTab, setActiveTab] = useState<AccessTab>(AccessTab.Users);
  const [createModalOpenFor, setCreateModalOpenFor] = useState<AccessTab | null>(null);

  const tabs: SegmentProps['options'] = [{ value: AccessTab.Users, label: AccessTab.Users }];

  const onClickCreate = () => setCreateModalOpenFor(activeTab);
  const onClickCloseCreate = () => setCreateModalOpenFor(null);

  return (
    <Container>
      <FlexRow>
        <Segment variant={SegmentVariant.Underline} options={tabs} selected={activeTab} setSelected={setActiveTab} />

        {isAdmin && (
          <PushRight>
            <Button leftIcon={PlusIcon} label={`create ${activeTab.substring(0, activeTab.length - 1)}`.toUpperCase()} onClick={onClickCreate} />
          </PushRight>
        )}
      </FlexRow>

      {renderActiveTab({ activeTab })}

      <CreateUserModal isOpen={createModalOpenFor === AccessTab.Users} onClose={onClickCloseCreate} />
    </Container>
  );
}
