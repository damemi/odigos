'use client';

import React, { useState } from 'react';
import styled from 'styled-components';
import Theme from '@odigos/ui-kit/theme';
import { useConnectionsCRUD } from '@/hooks';
import { PlusIcon } from '@odigos/ui-kit/icons';
import { TeamsTable } from '@/components/tables';
import { Connection } from '@odigos/ui-kit/types';
import { ComputePlatforms } from '@odigos/ui-kit/containers';
import { CreateTeamModal } from '@/components/modal/create-team-modal';
import { UsersTable, CreateUserModal, OverviewHeader } from '@/components';
import { FlexColumn, Button, TabList, Tab } from '@odigos/ui-kit/components';

export const PageWrapper = styled(FlexColumn)`
  width: 100%;
`;

const PushRight = styled.div`
  margin-left: auto;
`;

const TableWrapper = styled.div`
  padding: 24px;
`;

enum AccessTab {
  Users = 'Users',
  Teams = 'Teams',
  Compute = 'Compute Platforms',
}

const renderActiveTab = ({ activeTab, connections }: { activeTab: AccessTab; connections: Connection[] }) => {
  const tabFactories: Record<AccessTab, () => React.ReactNode> = {
    [AccessTab.Compute]: () => <ComputePlatforms connections={connections} onSelect={() => {}} />,
    [AccessTab.Teams]: () => <TeamsTable />,
    [AccessTab.Users]: () => <UsersTable />,
  };

  return (tabFactories[activeTab] || tabFactories[AccessTab.Users])();
};

export default function Page() {
  const theme = Theme.useTheme();
  const { connections } = useConnectionsCRUD();
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
    <PageWrapper>
      <OverviewHeader />

      <TableWrapper>
        <FlexColumn>
          <TabList tabs={tabs} />

          {activeTab !== AccessTab.Compute && (
            <PushRight>
              <Button onClick={onClickCreate}>
                <PlusIcon fill={theme.text.primary} />
                {`create ${activeTab.substring(0, activeTab.length - 1)}`.toUpperCase()}
              </Button>
            </PushRight>
          )}
        </FlexColumn>

        {renderActiveTab({ activeTab, connections })}
      </TableWrapper>

      <CreateUserModal isOpen={createModalOpenFor === AccessTab.Users} onClose={onClickCloseCreate} />
      <CreateTeamModal isOpen={createModalOpenFor === AccessTab.Teams} onClose={onClickCloseCreate} />
    </PageWrapper>
  );
}
