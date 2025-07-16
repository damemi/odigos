import styled from 'styled-components';
import { FlexColumn, FlexRow, Text } from '@odigos/ui-kit/components';

export const DataCardContent = styled(FlexRow)`
  width: 100%;
  gap: 36px;
`;

export const DataCardContentCell = styled(FlexColumn)`
  line-height: 30px;
`;

export const DataCardCellTitle = styled(Text)`
  color: ${({ theme }) => theme.text.grey};
  font-size: 12px;
`;

export const DataCardCellStretch = styled(Text)`
  width: 250px;
`;
