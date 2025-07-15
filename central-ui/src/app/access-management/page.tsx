'use client';

import React, { useState } from 'react';
import styled from 'styled-components';
import * as STRINGS from '@/utils';
import Theme from '@odigos/ui-kit/theme';
import { useConnectionsCRUD } from '@/hooks';
import { PlusIcon } from '@odigos/ui-kit/icons';
import { Connection } from '@odigos/ui-kit/types';
import { TeamsTable } from '@/components/tables/teams';
import { ComputePlatforms } from '@odigos/ui-kit/containers';
import { FlexColumn, Button } from '@odigos/ui-kit/components';
import { CreateTeamModal } from '@/components/modal/create-team-modal';
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

  const tabs: TabItem[] = [
    { key: AccessTab.Users, label: STRINGS.USERS_TAB, active: activeTab === AccessTab.Users, onClick: () => setActiveTab(AccessTab.Users) },
    { key: AccessTab.Teams, label: STRINGS.TEAMS_TAB, active: activeTab === AccessTab.Teams, onClick: () => setActiveTab(AccessTab.Teams) },
    { key: AccessTab.Compute, label: STRINGS.COMPUTE_PLATFORMS_TAB, active: activeTab === AccessTab.Compute, onClick: () => setActiveTab(AccessTab.Compute) },
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
            <CreateButton onClick={onClickCreate}>
              <PlusIcon fill={theme.text.primary} />
              {`create ${activeTab.substring(0, activeTab.length - 1)}`.toUpperCase()}
            </CreateButton>
          )}
        </FlexColumn>

        {renderActiveTab({ activeTab, connections })}
      </TableWrapper>

      <CreateUserModal isOpen={createModalOpenFor === AccessTab.Users} onClose={onClickCloseCreate} />
      <CreateTeamModal isOpen={createModalOpenFor === AccessTab.Teams} onClose={onClickCloseCreate} />
    </PageWrapper>
  );
}
