import React, { useState } from 'react';
import { StatusType } from '@odigos/ui-kit/types';
import { CopyText } from '@odigos/ui-kit/snippets';
import { NotificationNote } from '@odigos/ui-kit/components';
import { SuccessContainer, SuccessTitle, SuccessSubtitle, PasswordInputWrapper, DoneButton } from './create-user-modal.styled';
import { USER_CREATED_SUCCESS_TITLE, USER_CREATED_SUCCESS_SUBTITLE, USER_CREATED_SUCCESS_PASSWORD_NOTE, USER_CREATED_SUCCESS_DONE_LABEL } from '@/utils';

interface UserCreatedSuccessfullyProps {
  email: string;
  password: string;
  onDone: () => void;
}

export const UserCreatedSuccessfully: React.FC<UserCreatedSuccessfullyProps> = ({ email, password, onDone }) => {
  const [done, setDone] = useState(false);

  return (
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
        <CopyText value={password} callbackCopyDone={() => setDone(true)} />
      </PasswordInputWrapper>
      <NotificationNote type={StatusType.Default} title={USER_CREATED_SUCCESS_PASSWORD_NOTE} />

      <DoneButton type='button' onClick={onDone} disabled={!done}>
        {USER_CREATED_SUCCESS_DONE_LABEL}
      </DoneButton>
    </SuccessContainer>
  );
};
