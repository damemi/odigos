import styled from 'styled-components';
import { FlexColumn, FlexRow, Text } from '@odigos/ui-kit/components';

export const DataCardContentRow = styled(FlexRow)`
  width: 100%;
  gap: 36px;
  align-items: flex-start;
`;

export const DataCardContentColumn = styled(FlexColumn)`
  width: 100%;
  gap: 12px;
`;

export const DataCardContentCell = styled(FlexColumn)`
  line-height: 30px;
`;

export const DataCardCellTitle = styled(Text)`
  color: ${({ theme }) => theme.text.grey};
  font-size: 12px;
`;

export const DataCardCellStretch = styled(Text)`
  width: 300px;
`;
