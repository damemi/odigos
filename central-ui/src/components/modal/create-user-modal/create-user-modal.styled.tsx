import styled from 'styled-components';
import { Button, Text } from '@odigos/ui-kit/components';

export const ModalContent = styled.div`
  padding: 24px;
  min-width: 734px;
`;

export const Form = styled.form`
  display: flex;
  flex-direction: column;
  gap: 16px;
`;

export const ButtonContainer = styled.div`
  display: flex;
  gap: 12px;
  justify-content: flex-end;
  margin-top: 8px;
`;

export const CancelButton = styled(Button)`
  background-color: transparent;
  color: ${({ theme }) => theme.text.primary};
  border: 1px solid ${({ theme }) => theme.colors.border};

  &:hover {
    background-color: ${({ theme }) => theme.colors.translucent_bg};
  }
`;

export const CreateButton = styled(Button)``;

export const RoleDescriptionContainer = styled.div`
  padding: 12px;
  background-color: ${({ theme }) => theme.colors.translucent_bg};
  border-radius: 6px;
  border-left: 3px solid ${({ theme }) => theme.colors.info};
`;

export const RoleDescriptionText = styled(Text)`
  margin: 0;
  line-height: 1.4;
`;

export const DescriptionTextWrapper = styled.div`
  display: flex;
  flex-direction: column;
  gap: 8px;
  opacity: 0.8;
`;

export const Row = styled.div`
  display: flex;
  gap: 16px;
`;

export const Half = styled.div`
  flex: 1;
`;

export const SuccessContainer = styled.div`
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 16px;
`;

export const SuccessTitle = styled(Text)`
  text-align: center;
  font-size: 20px;
  font-style: normal;
  font-weight: 600;
  line-height: 28px;
`;

export const SuccessSubtitle = styled(Text)`
  color: var(--color-text-medium, #b8b8b8);
  text-align: center;
  font-size: 14px;
  line-height: 24px;
`;

export const PasswordInputWrapper = styled.div`
  display: flex;
  padding: 4px 8px 4px 16px;
  align-items: center;
  gap: 24px;
  flex: 1 0 0;
  border-radius: 8px;
  background: #525252;
  color: #aabef7;
  font-size: 16px;
  font-weight: 500;
  line-height: 24px;
  max-width: 360px;
`;

export const PasswordInput = styled.input`
  font-family: ${({ theme }) => theme.font_family.code};
  font-size: 16px;
  font-weight: 500;
  line-height: 24px;
  outline: none;
  flex: 1;
  background: transparent;
  border: none;
  color: #aabef7;
`;

export const DoneButton = styled(Button)`
  width: 180px;
`;
