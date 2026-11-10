import styled from 'styled-components';

// Local replacement for the (no longer exported) v1 `IconWrapped`.
// Renders a small rounded square containing an icon, sized to match
// what the v1 `IconWrapped` produced in the table's first column.
export const IconCircle = styled.div`
  display: flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border-radius: 8px;
  background: ${({ theme }) => theme.v2.colors.silver['800']};
`;
