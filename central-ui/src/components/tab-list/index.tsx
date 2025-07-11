import React from 'react';
import { TabListContainer, Tab } from './tab-list.styled';

export interface TabItem {
  key: string;
  label: string;
  icon?: React.ReactNode;
  active?: boolean;
  onClick?: () => void;
}

export interface TabListProps {
  tabs: TabItem[];
  className?: string;
}

export const TabList: React.FC<TabListProps> = ({ tabs, className }) => {
  return (
    <TabListContainer className={className}>
      {tabs.map(({ key, label, icon, active, onClick }) => (
        <Tab key={key} $selected={!!active} onClick={onClick}>
          {icon && <span style={{ display: 'flex', alignItems: 'center' }}>{icon}</span>}
          {label}
        </Tab>
      ))}
    </TabListContainer>
  );
};
