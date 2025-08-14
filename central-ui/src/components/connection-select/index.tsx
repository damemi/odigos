import React, { useRef, useState } from 'react';
import Theme from '@odigos/ui-kit/theme';
import type { ComputePlatform } from '@/types';
import styled, { css } from 'styled-components';
import { StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useComputePlatformsCRUD, useProxyID } from '@/hooks';
import { OverviewIcon, SearchIcon } from '@odigos/ui-kit/icons';
import { useKeyDown, useOnClickOutside } from '@odigos/ui-kit/hooks';
import { getPlatformIcon, getPlatformLabel } from '@odigos/ui-kit/functions';
import { Button, ExtendArrow, FlexRow, Input, SelectionButton, Text, VerticalScroll } from '@odigos/ui-kit/components';

const Tab = styled(FlexRow)<{ $withSelect: boolean }>`
  width: 260px;
  padding: 4px;
  gap: 10px;
  border: 1px solid ${({ theme }) => theme.colors.border};
  border-radius: 32px;
  cursor: ${({ $withSelect }) => ($withSelect ? 'pointer' : 'default')};

  ${({ $withSelect }) =>
    $withSelect &&
    css`
      &:hover {
        border: 1px solid ${({ theme }) => theme.colors.secondary};
      }
    `}
`;

const Title = styled(Text)`
  font-size: 14px;
  margin-right: 10px;
  color: ${({ theme }) => theme.text.secondary};
`;

const LogoWrap = styled.div`
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 4px;
  border-radius: 100%;
  background-color: ${({ theme }) => theme.colors.info};
`;

const PushToEnd = styled.div`
  margin-left: auto;
  margin-right: 6px;
`;

const RelativeContainer = styled.div`
  position: relative;
`;

const AbsoluteContainer = styled.div`
  position: absolute;
  top: calc(100% + 8px);
  left: 0;
  z-index: 1;
  background-color: ${({ theme }) => theme.colors.dropdown_bg};
  border: ${({ theme }) => `1px solid ${theme.colors.border}`};
  border-radius: 24px;
  width: 420px;
`;

const HeadWrap = styled.div`
  border-bottom: ${({ theme }) => `1px solid ${theme.colors.border}`};
  padding: 12px;
`;

const FootWrap = styled.div`
  border-top: ${({ theme }) => `1px solid ${theme.colors.border}`};
  padding: 12px;
`;

export const ConnectionSelect: React.FC = () => {
  const theme = Theme.useTheme();
  const { addNotification } = useNotificationStore();
  const { proxyID, goSelectNewProxy, selectProxyID } = useProxyID();
  const { computePlatforms, currentComputePlatform } = useComputePlatformsCRUD(proxyID);

  const [isOpen, setIsOpen] = useState(false);
  const [searchText, setSearchText] = useState('');

  const onClose = () => {
    setIsOpen(false);
  };

  const onClickConnection = ({ id }: ComputePlatform) => selectProxyID(id);
  const onClickAll = () => goSelectNewProxy();

  const containerRef = useRef<HTMLDivElement>(null);
  useOnClickOutside(containerRef, onClose);
  useKeyDown({ key: 'Escape', active: isOpen }, onClose);

  const withSelect = !!computePlatforms.length;
  const filtered = computePlatforms.filter(({ id }) => id.toLowerCase().includes(searchText));
  const Icon = !!currentComputePlatform?.type ? getPlatformIcon(currentComputePlatform.type) : null;

  return (
    <RelativeContainer ref={containerRef}>
      <Tab $withSelect={withSelect} onClick={() => setIsOpen((prev) => !prev)}>
        <LogoWrap>{!!Icon ? <Icon size={20} fill={theme.text.info} /> : <OverviewIcon fill={theme.text.info} />}</LogoWrap>
        <Title>{currentComputePlatform?.name || currentComputePlatform?.id || 'no platform'}</Title>

        {withSelect && (
          <PushToEnd>
            <ExtendArrow extend={isOpen} align='right' />
          </PushToEnd>
        )}
      </Tab>

      {isOpen && withSelect && (
        <AbsoluteContainer>
          <HeadWrap>
            <Input placeholder='Search...' icon={SearchIcon} value={searchText} onChange={(e) => setSearchText(e.target.value.toLowerCase())} />
          </HeadWrap>

          <VerticalScroll style={{ maxHeight: '240px' }}>
            {filtered.map(({ id, type, name, status }, idx) => (
              <SelectionButton
                key={`platform-${id}`}
                icon={() => getPlatformIcon(type)?.({ fill: status === StatusType.Success ? theme.text.success : theme.text.error })}
                label={`${!!name ? name : getPlatformLabel(type)} (${id})`}
                isSelected={currentComputePlatform?.id === id}
                onClick={() => {
                  if (status === StatusType.Success) {
                    onClickConnection(filtered[idx]);
                  } else {
                    addNotification({
                      type: StatusType.Warning,
                      title: 'Connection lost',
                      message: 'Cannot connect to this platform',
                      hideFromHistory: true,
                    });
                  }
                  onClose();
                }}
                color='transparent'
                style={{ width: '100%', justifyContent: 'flex-start' }}
              />
            ))}
          </VerticalScroll>

          <FootWrap>
            <Button variant='secondary' style={{ width: '100%' }} onClick={onClickAll}>
              VIEW ALL
            </Button>
          </FootWrap>
        </AbsoluteContainer>
      )}
    </RelativeContainer>
  );
};
