import styled from 'styled-components';
import { Typography } from '@odigos/ui-kit/components/v2';
import { FlexColumn, FlexRow } from '@odigos/ui-kit/components';

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

export const DataCardCellTitle = styled(Typography)`
  color: ${({ theme }) => theme.v2.colors.silver['400']};
  font-size: 12px;
`;

export const DataCardCellStretch = styled.div`
  width: 300px;
`;

// Custom card layout matching v2 DataCard styling, but with a free-form action area
// in the header (v2 DataCard's `actions` prop only accepts typed buttons/dropdowns,
// which doesn't fit the search + add-popup pattern used by the list cards below).
export const ListCard = styled.div`
  display: flex;
  flex-direction: column;
  gap: 12px;
  width: calc(100% - 24px);
  padding: 12px;
  border-radius: 12px;
  background-color: ${({ theme }) => theme.v2.colors.silver['900']};
`;

export const ListCardHeader = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  width: 100%;
`;

export const ListCardHeaderLeft = styled.div`
  display: flex;
  align-items: center;
  gap: 6px;
`;

export const ListCardHeaderRight = styled.div`
  display: flex;
  align-items: center;
  gap: 8px;
`;

// Replacement for the (no longer exported) v1 `DataFinger` row.
export const ListRow = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px;
  border-radius: 8px;
  background-color: ${({ theme }) => theme.v2.colors.silver['800']};
`;

export const ListRowMain = styled.div`
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
`;

export const ListRowText = styled.div`
  display: flex;
  flex-direction: column;
  min-width: 0;
`;
