import React, { FC } from 'react';
import { OidcProviderId } from '@/types';
import { EmailIcon } from '@odigos/ui-kit/icons';
import * as STRINGS from '@/utils/constants/strings';
import { FlexColumn } from '@odigos/ui-kit/components';
import { ButtonTab } from '@odigos/ui-kit/components/v2';
import { ConstrainedWidth, FormContainer, FormSubtitle, FormTitle } from '../styled';

interface ChooseFormProps {
  isFirstTime: boolean;
  loading: boolean;
  onClick: (providerId: OidcProviderId) => void;
}

const ChooseForm: FC<ChooseFormProps> = ({ isFirstTime, loading, onClick }) => {
  return (
    <FormContainer $gap={121}>
      <FlexColumn $gap={16}>
        <FormTitle>{isFirstTime ? STRINGS.SIGN_UP_ADMIN_TITLE : STRINGS.SIGN_IN}</FormTitle>
        <FormSubtitle>{isFirstTime ? STRINGS.SIGN_UP_ADMIN_SUBTITLE_1 : STRINGS.SIGN_IN_SUBTITLE_1}</FormSubtitle>
      </FlexColumn>

      <ConstrainedWidth>
        <FlexColumn $gap={12}>
          <ButtonTab label={`${STRINGS.CONTINUE_WITH} ${STRINGS.EMAIL}`} icon={EmailIcon} loading={loading} onClick={() => onClick(OidcProviderId.EMAIL)} />
          {/* TODO: uncomment this when OIDC providers are supported (need roles & groups to be implemented) */}
          {/* {OIDC_PROVIDERS.map((provider) => (
            <ButtonTab key={provider.providerId} label={`${STRINGS.CONTINUE_WITH} ${provider.displayName}`} icon={provider.icon} loading={loading} onClick={() => onClick(provider.providerId)} />
          ))} */}
        </FlexColumn>
      </ConstrainedWidth>
    </FormContainer>
  );
};

export { ChooseForm, type ChooseFormProps };
