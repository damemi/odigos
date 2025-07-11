import styled from 'styled-components';
import { Text } from '@odigos/ui-kit/components';

export const TabListContainer = styled.div`
  display: flex;
  flex: 1;
  align-items: center;
  gap: 8px;
  border-bottom: 1px solid ${({ theme }) => theme.colors.dropdown_bg_2};
`;

export const Tab = styled(Text)<{ $selected?: boolean }>`
  padding: 12px;
  font-size: 14px;
  color: ${({ theme, $selected }) => ($selected ? theme.text.secondary : theme.text.grey)};
  border-bottom: 2px solid ${({ theme, $selected }) => ($selected ? theme.colors.majestic_blue : 'transparent')};
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 8px;
  background: none;
`;
