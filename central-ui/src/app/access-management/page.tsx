'use client';

import React, { useState } from 'react';
import { useUserStore } from '@/store';
import { FlexRow } from '@odigos/ui-kit/components';
import styled from 'styled-components';
import { UsersTable, CreateUserModal, RoleMappingsTable, CreateRoleMappingModal } from '@/components';
import { Button, ButtonSize, Segment, type SegmentProps, SegmentVariant } from '@odigos/ui-kit/components';

const Container = styled.div`
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 24px;
  height: stretch;
  padding: 12px;
`;

const PushRight = styled.div`
  margin-left: auto;
`;

enum AccessTab {
  Users = 'Users',
  RoleMappings = 'Role Mappings',
  // Teams = 'Teams',
  // Compute = 'Compute Platforms',
}

const renderActiveTab = ({ activeTab }: { activeTab: AccessTab }) => {
  const tabFactories: Record<AccessTab, () => React.ReactNode> = {
    [AccessTab.Users]: () => <UsersTable />,
    [AccessTab.RoleMappings]: () => <RoleMappingsTable />,
    // [AccessTab.Teams]: () => <TeamsTable />,
    // [AccessTab.Compute]: () => <ComputePlatformsTable />,
  };

  return (tabFactories[activeTab] || tabFactories[AccessTab.Users])();
};

export default function Page() {
  const { isAdmin } = useUserStore();
  const [activeTab, setActiveTab] = useState<AccessTab>(AccessTab.Users);
  const [createModalOpenFor, setCreateModalOpenFor] = useState<AccessTab | null>(null);

  const tabs: SegmentProps['options'] = [
    { value: AccessTab.Users, label: AccessTab.Users },
    { value: AccessTab.RoleMappings, label: AccessTab.RoleMappings },
  ];

  const onClickCreate = () => setCreateModalOpenFor(activeTab);
  const onClickCloseCreate = () => setCreateModalOpenFor(null);

  return (
    <Container>
      <FlexRow>
        <Segment variant={SegmentVariant.Underline} options={tabs} selected={activeTab} setSelected={setActiveTab} />

        {isAdmin && (
          <PushRight>
            <Button size={ButtonSize.S} label={`Create ${activeTab.substring(0, activeTab.length - 1)}`} onClick={onClickCreate} />
          </PushRight>
        )}
      </FlexRow>

      {renderActiveTab({ activeTab })}

      <CreateUserModal isOpen={createModalOpenFor === AccessTab.Users} onClose={onClickCloseCreate} />
      <CreateRoleMappingModal isOpen={createModalOpenFor === AccessTab.RoleMappings} onClose={onClickCloseCreate} />
    </Container>
  );
}
