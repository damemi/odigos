'use client';

import React, { useState } from 'react';
import { useUserStore } from '@/store';
import styled from 'styled-components';
import { UsersTable, CreateUserModal, RoleMappingsTable, CreateRoleMappingModal, IdentityProvidersTable } from '@/components';
import { Button, ButtonSize, FlexRow, PageContent, Segment, type SegmentProps, SegmentVariant } from '@odigos/ui-kit/components';

const PushRight = styled.div`
  margin-left: auto;
`;

enum AccessTab {
  Users = 'Users',
  RoleMappings = 'Role Mappings',
  IdentityProviders = 'Identity Providers',
  // Teams = 'Teams',
  // Compute = 'Compute Platforms',
}

const renderActiveTab = ({ activeTab }: { activeTab: AccessTab }) => {
  const tabFactories: Record<AccessTab, () => React.ReactNode> = {
    [AccessTab.Users]: () => <UsersTable />,
    [AccessTab.RoleMappings]: () => <RoleMappingsTable />,
    [AccessTab.IdentityProviders]: () => <IdentityProvidersTable />,
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
    // Identity providers are managed by ADMIN/OWNER only; the backend enforces the same.
    ...(isAdmin ? [{ value: AccessTab.IdentityProviders, label: AccessTab.IdentityProviders }] : []),
  ];

  // Fall back to Users if the role changes while the Admin-only tab is open.
  const visibleTab = activeTab === AccessTab.IdentityProviders && !isAdmin ? AccessTab.Users : activeTab;
  // Identity providers have one fixed row per supported provider, so there is nothing to create.
  const canCreate = isAdmin && visibleTab !== AccessTab.IdentityProviders;

  const onClickCreate = () => setCreateModalOpenFor(visibleTab);
  const onClickCloseCreate = () => setCreateModalOpenFor(null);

  return (
    <PageContent>
      <FlexRow>
        <Segment variant={SegmentVariant.Underline} options={tabs} selected={visibleTab} setSelected={setActiveTab} />

        {canCreate && (
          <PushRight>
            <Button size={ButtonSize.S} label={`Create ${visibleTab.substring(0, visibleTab.length - 1)}`} onClick={onClickCreate} />
          </PushRight>
        )}
      </FlexRow>

      {renderActiveTab({ activeTab: visibleTab })}

      <CreateUserModal isOpen={createModalOpenFor === AccessTab.Users} onClose={onClickCloseCreate} />
      <CreateRoleMappingModal isOpen={createModalOpenFor === AccessTab.RoleMappings} onClose={onClickCloseCreate} />
    </PageContent>
  );
}
