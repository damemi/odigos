import React from 'react';
import { StatusType } from '@odigos/ui-kit/types';
import { CheckIcon, CopyIcon } from '@odigos/ui-kit/icons';
import { IconButton, NotificationNote } from '@odigos/ui-kit/components';
import { SuccessContainer, SuccessTitle, SuccessSubtitle, PasswordInputWrapper, PasswordInput, DoneButton } from './create-user-modal.styled';
import { USER_CREATED_SUCCESS_TITLE, USER_CREATED_SUCCESS_SUBTITLE, USER_CREATED_SUCCESS_PASSWORD_NOTE, USER_CREATED_SUCCESS_DONE_LABEL, COPY_TOOLTIP, COPIED_TOOLTIP } from '@/utils';

interface UserCreatedSuccessfullyProps {
  email: string;
  password: string;
  isCopied: boolean;
  onCopy: () => void;
  onDone: () => void;
}

export const UserCreatedSuccessfully: React.FC<UserCreatedSuccessfullyProps> = ({ email, password, isCopied, onCopy, onDone }) => (
  <SuccessContainer>
    <SuccessTitle>{USER_CREATED_SUCCESS_TITLE}</SuccessTitle>
    <SuccessSubtitle>
      {USER_CREATED_SUCCESS_SUBTITLE(email)
        .split('\n')
        .map((line, i) => (
          <React.Fragment key={i}>
            {line}
            <br />
          </React.Fragment>
        ))}
    </SuccessSubtitle>
    <PasswordInputWrapper>
      <PasswordInput value={password} readOnly />
      <IconButton onClick={onCopy} tooltip={isCopied ? COPIED_TOOLTIP : COPY_TOOLTIP}>
        {isCopied ? <CheckIcon /> : <CopyIcon />}
      </IconButton>
    </PasswordInputWrapper>
    <NotificationNote type={StatusType.Default} title={USER_CREATED_SUCCESS_PASSWORD_NOTE} />
    <DoneButton type='button' onClick={onDone} disabled={!isCopied}>
      {USER_CREATED_SUCCESS_DONE_LABEL}
    </DoneButton>
  </SuccessContainer>
);
