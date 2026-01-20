'use client';

import React, { useState } from 'react';
import { useUserStore } from '@/store';
import { PlusIcon } from '@odigos/ui-kit/icons';
import styled, { useTheme } from 'styled-components';
import { UsersTable, CreateUserModal } from '@/components';
import { CreateTeamModal } from '@/components/modal/create-team-modal';
import { ComputePlatformsTable, TeamsTable } from '@/components/tables';
import { Button, TabList, Tab, FlexRow } from '@odigos/ui-kit/components';

const Container = styled.div`
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 24px;
`;

const PushRight = styled.div`
  margin-left: auto;
`;

enum AccessTab {
  Users = 'Users',
  Teams = 'Teams',
  Compute = 'Compute Platforms',
}

const renderActiveTab = ({ activeTab }: { activeTab: AccessTab }) => {
  const tabFactories: Record<AccessTab, () => React.ReactNode> = {
    [AccessTab.Users]: () => <UsersTable />,
    [AccessTab.Teams]: () => <TeamsTable />,
    [AccessTab.Compute]: () => <ComputePlatformsTable />,
  };

  return (tabFactories[activeTab] || tabFactories[AccessTab.Users])();
};

export default function Page() {
  const theme = useTheme();
  const { isAdmin } = useUserStore();
  const [activeTab, setActiveTab] = useState<AccessTab>(AccessTab.Users);
  const [createModalOpenFor, setCreateModalOpenFor] = useState<AccessTab | null>(null);

  const tabs: Tab[] = [
    { label: AccessTab.Users, selected: activeTab === AccessTab.Users, onClick: () => setActiveTab(AccessTab.Users) },
    { label: AccessTab.Teams, selected: activeTab === AccessTab.Teams, onClick: () => setActiveTab(AccessTab.Teams) },
    { label: AccessTab.Compute, selected: activeTab === AccessTab.Compute, onClick: () => setActiveTab(AccessTab.Compute) },
  ];

  const onClickCreate = () => setCreateModalOpenFor(activeTab);
  const onClickCloseCreate = () => setCreateModalOpenFor(null);

  return (
    <Container>
      <FlexRow>
        <TabList tabs={tabs} />

        {activeTab !== AccessTab.Compute && isAdmin && (
          <PushRight>
            <Button onClick={onClickCreate}>
              <PlusIcon fill={theme.text.primary} />
              {`create ${activeTab.substring(0, activeTab.length - 1)}`.toUpperCase()}
            </Button>
          </PushRight>
        )}
      </FlexRow>

      {renderActiveTab({ activeTab })}

      <CreateUserModal isOpen={createModalOpenFor === AccessTab.Users} onClose={onClickCloseCreate} />
      <CreateTeamModal isOpen={createModalOpenFor === AccessTab.Teams} onClose={onClickCloseCreate} />
    </Container>
  );
}
