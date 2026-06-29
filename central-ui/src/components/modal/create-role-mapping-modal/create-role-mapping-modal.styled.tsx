import styled from 'styled-components';

export const Container = styled.div`
  min-width: 734px;
`;

export const Header = styled.div`
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 24px 24px 0;
`;

export const DescriptionTextWrapper = styled.div`
  display: flex;
  flex-direction: column;
  gap: 8px;
  opacity: 0.8;
`;

export const Form = styled.form`
  display: flex;
  flex-direction: column;
`;

export const Body = styled.div`
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 24px;
`;

export const Row = styled.div`
  display: flex;
  gap: 16px;
`;

export const Half = styled.div`
  flex: 1;
`;

// Full-width footer action bar, mirroring the kit's WarningModal footer so the
// background spans edge-to-edge of the modal.
export const Actions = styled.div`
  display: flex;
  justify-content: center;
  gap: 16px;
  padding: 16px 24px;
  width: 100%;
  background-color: ${({ theme }) => theme.v2.colors.silver['1000']};
`;
