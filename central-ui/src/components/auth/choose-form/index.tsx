import React, { FC, useMemo } from 'react';
import { EmailIcon } from '@odigos/ui-kit/icons';
import { AuthProviderId, LoginType } from '@/types';
import * as STRINGS from '@/utils/constants/strings';
import { FlexColumn } from '@odigos/ui-kit/components';
import { ButtonTab } from '@odigos/ui-kit/components/v2';
import { AUTH_PROVIDERS } from '@/utils/constants/auth-providers';
import { ConstrainedWidth, FormContainer, FormSubtitle, FormTitle } from '../styled';

interface ChooseFormProps {
  isFirstTime: boolean;
  loading: boolean;
  onClick: (providerId: AuthProviderId, loginType?: LoginType) => void;
}

const ChooseForm: FC<ChooseFormProps> = ({ isFirstTime, loading, onClick }) => {
  const authProvidersBtnsToRender = useMemo(
    () =>
      AUTH_PROVIDERS.map((provider) => (
        <ButtonTab
          key={provider.providerId}
          label={`${STRINGS.CONTINUE_WITH} ${provider.displayName}`}
          icon={provider.icon}
          loading={loading}
          onClick={() => onClick(provider.providerId, provider.loginType)}
        />
      )),
    [loading],
  );

  return (
    <FormContainer $gap={121}>
      <FlexColumn $gap={16}>
        <FormTitle>{isFirstTime ? STRINGS.SIGN_UP_ADMIN_TITLE : STRINGS.SIGN_IN}</FormTitle>
        <FormSubtitle>{isFirstTime ? STRINGS.SIGN_UP_ADMIN_SUBTITLE_1 : STRINGS.SIGN_IN_SUBTITLE_1}</FormSubtitle>
      </FlexColumn>

      <ConstrainedWidth>
        <FlexColumn $gap={12}>
          <ButtonTab label={`${STRINGS.CONTINUE_WITH} ${STRINGS.EMAIL}`} icon={EmailIcon} loading={loading} onClick={() => onClick(AuthProviderId.EMAIL)} />
          {authProvidersBtnsToRender}
        </FlexColumn>
      </ConstrainedWidth>
    </FormContainer>
  );
};

export { ChooseForm, type ChooseFormProps };
