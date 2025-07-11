'use client';

import React, { useState } from 'react';
import styled from 'styled-components';
import { User } from '@/types';
import * as STRINGS from '@/utils';
import { Connection } from '@odigos/ui-kit/types';
import { useConnectionsCRUD, useUsersCRUD } from '@/hooks';
import { ComputePlatforms } from '@odigos/ui-kit/containers';
import { FlexColumn, Button, Text } from '@odigos/ui-kit/components';
import { TabList, UsersTable, CreateUserModal, type TabItem, OverviewHeader } from '@/components';

export const PageWrapper = styled(FlexColumn)`
  width: 100%;
`;

const CreateButton = styled(Button)`
  margin: 10px 0 0 auto;
`;

const TableWrapper = styled.div`
  padding: 24px;
`;

enum AccessTab {
  Users = 'users',
  Teams = 'teams',
  Compute = 'compute',
}

function renderActiveTab({ activeTab, users, connections }: { activeTab: AccessTab; users: User[]; connections: Connection[] }) {
  const tabFactories: Record<AccessTab, () => React.ReactNode> = {
    [AccessTab.Compute]: () => <ComputePlatforms connections={connections} onSelect={() => {}} />,
    [AccessTab.Teams]: () => <Text>WIP</Text>,
    [AccessTab.Users]: () => <UsersTable users={users} />,
  };
  return (tabFactories[activeTab] || tabFactories[AccessTab.Users])();
}

export default function Page() {
  const { users } = useUsersCRUD();
  const { connections } = useConnectionsCRUD();
  const [activeTab, setActiveTab] = useState<AccessTab>(AccessTab.Users);
  const [isCreateUserModalOpen, setIsCreateUserModalOpen] = useState(false);

  const tabs: TabItem[] = [
    { key: AccessTab.Users, label: STRINGS.USERS_TAB, active: activeTab === AccessTab.Users, onClick: () => setActiveTab(AccessTab.Users) },
    // { key: AccessTab.Teams, label: STRINGS.TEAMS_TAB, active: activeTab === AccessTab.Teams, onClick: () => setActiveTab(AccessTab.Teams) },
    { key: AccessTab.Compute, label: STRINGS.COMPUTE_PLATFORMS_TAB, active: activeTab === AccessTab.Compute, onClick: () => setActiveTab(AccessTab.Compute) },
  ];

  const handleCreateClick = () => {
    if (activeTab === AccessTab.Users) {
      setIsCreateUserModalOpen(true);
    }
    // TODO: Handle create team when Teams tab is active, or compute platform when Compute tab is active
  };

  const handleCloseCreateUserModal = () => {
    setIsCreateUserModalOpen(false);
  };

  const getCreateButtonText = () => {
    if (activeTab === AccessTab.Users) {
      return STRINGS.CREATE_USER_BUTTON;
    }
    return STRINGS.CREATE_TEAM_BUTTON;
  };

  return (
    <PageWrapper>
      <OverviewHeader />
      <TableWrapper>
        <FlexColumn>
          <TabList tabs={tabs} />
          {activeTab !== AccessTab.Compute && <CreateButton onClick={handleCreateClick}>{getCreateButtonText()}</CreateButton>}
        </FlexColumn>
        {renderActiveTab({ activeTab, users, connections })}
      </TableWrapper>

      <CreateUserModal isOpen={isCreateUserModalOpen} onClose={handleCloseCreateUserModal} />
    </PageWrapper>
  );
}
