'use client';

import React, { useState } from 'react';
import styled from 'styled-components';
import { useUserStore } from '@/store';
import Theme from '@odigos/ui-kit/theme';
import { PlusIcon } from '@odigos/ui-kit/icons';
import { CreateTeamModal } from '@/components/modal/create-team-modal';
import { ComputePlatformsTable, TeamsTable } from '@/components/tables';
import { UsersTable, CreateUserModal, OverviewHeader } from '@/components';
import { FlexColumn, Button, TabList, Tab } from '@odigos/ui-kit/components';

const Padding = styled.div`
  padding: 24px;
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
  const theme = Theme.useTheme();
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
    <FlexColumn>
      <OverviewHeader />

      <Padding>
        <FlexColumn>
          <TabList tabs={tabs} />

          {activeTab !== AccessTab.Compute && isAdmin && (
            <PushRight>
              <Button onClick={onClickCreate}>
                <PlusIcon fill={theme.text.primary} />
                {`create ${activeTab.substring(0, activeTab.length - 1)}`.toUpperCase()}
              </Button>
            </PushRight>
          )}
        </FlexColumn>

        {renderActiveTab({ activeTab })}
      </Padding>

      <CreateUserModal isOpen={createModalOpenFor === AccessTab.Users} onClose={onClickCloseCreate} />
      <CreateTeamModal isOpen={createModalOpenFor === AccessTab.Teams} onClose={onClickCloseCreate} />
    </FlexColumn>
  );
}
