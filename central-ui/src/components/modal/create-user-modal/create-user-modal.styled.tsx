import styled from 'styled-components';
import { Button, Typography } from '@odigos/ui-kit/components/v2';

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

export const RoleDescriptionText = styled(Typography)`
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

export const SuccessTitle = styled(Typography)`
  text-align: center;
  font-size: 20px;
  font-style: normal;
  font-weight: 600;
  line-height: 28px;
`;

export const SuccessSubtitle = styled(Typography)`
  color: var(--color-text-medium, #b8b8b8);
  text-align: center;
  font-size: 14px;
  line-height: 24px;
  white-space: pre-line;
`;

export const PasswordInputWrapper = styled.div`
  max-width: 360px;
`;

// Verbatim, case-preserving password field.
// Note: do NOT use the ui-kit CliCommand component here — it lowercases its value
// for both display and copy, which corrupts the case-sensitive generated password.
export const PasswordBox = styled.div`
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  max-width: 360px;
  padding: 8px 12px;
  border-radius: 8px;
  background-color: ${({ theme }) => theme.colors.translucent_bg};
  border: 1px solid ${({ theme }) => theme.colors.border};
`;

export const PasswordText = styled(Typography)`
  flex: 1;
  font-family: monospace;
  font-size: 14px;
  letter-spacing: 0.5px;
  user-select: all;
  word-break: break-all;
`;

export const DoneButton = styled(Button)`
  width: 180px;
`;
