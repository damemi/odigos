import React, { FC, useMemo } from 'react';
import { EmailIcon } from '@odigos/ui-kit/icons';
import { AuthProviderId, LoginType } from '@/types';
import * as STRINGS from '@/utils/constants/strings';
import { FlexColumn } from '@odigos/ui-kit/components';
import { ButtonTab } from '@odigos/ui-kit/components';
import { AUTH_PROVIDERS } from '@/utils/constants/auth-providers';
import { ConstrainedWidth, FormContainer, FormSubtitle, FormTitle, ErrorText } from '../styled';

interface ChooseFormProps {
  isFirstTime: boolean;
  loading: boolean;
  /** When empty/undefined, show all IdP options (first-time setup). When non-empty, email + matching providers only. */
  configuredProviders?: AuthProviderId[];
  error?: string | null;
  onClick: (providerId: AuthProviderId, defaultLoginType?: LoginType) => void;
}

const ChooseForm: FC<ChooseFormProps> = ({ isFirstTime, loading, configuredProviders, error, onClick }) => {
  const providersToShow = useMemo(() => {
    if (configuredProviders && configuredProviders.length > 0) {
      return AUTH_PROVIDERS.filter((provider) => configuredProviders.includes(provider.providerId));
    }
    return AUTH_PROVIDERS;
  }, [configuredProviders]);

  const authProvidersBtnsToRender = useMemo(
    () =>
      providersToShow.map((provider) => (
        <ButtonTab
          key={provider.providerId}
          label={`${STRINGS.CONTINUE_WITH} ${provider.displayName}`}
          icon={provider.icon}
          loading={loading}
          onClick={() => onClick(provider.providerId, provider.defaultLoginType)}
        />
      )),
    [loading, onClick, providersToShow],
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
          {error && <ErrorText>{error}</ErrorText>}
        </FlexColumn>
      </ConstrainedWidth>
    </FormContainer>
  );
};

export { ChooseForm, type ChooseFormProps };
