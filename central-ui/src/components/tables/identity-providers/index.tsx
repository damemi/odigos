import React, { useCallback, useMemo, useState } from 'react';
import styled from 'styled-components';
import { IconCircle } from '../styled';
import { StatusType } from '@odigos/ui-kit/types';
import { useIdentityProvidersCRUD } from '@/hooks';
import { EditIdentityProviderDrawer } from '@/components';
import * as STRINGS from '@/utils/constants/strings';
import { AUTH_PROVIDERS } from '@/utils/constants/auth-providers';
import { IdentityProviderProtocol, type AuthProviderId, type IdentityProvider } from '@/types';
import { Note, Tag, TagVariants, Table, TableVariant, Typography, TypographyColor, TypographySize, type TableProps } from '@odigos/ui-kit/components';

// Table rows can't take keyboard focus, so the provider name is the keyboard entry point to the drawer.
const ProviderButton = styled.button`
  all: unset;
  cursor: pointer;
  border-radius: 4px;

  &:focus-visible {
    outline: 1px solid ${({ theme }) => theme.v2.colors.purple['500']};
    outline-offset: 4px;
  }
`;

// Client and entity IDs are opaque; monospace makes them easier to compare.
const IdentifierText = styled(Typography)`
  font-family: monospace;
`;

enum IdentityProviderTableColumn {
  Icon = 'icon',
  Provider = 'provider',
  Protocol = 'protocol',
  Status = 'status',
  Identifier = 'identifier',
}

const PROTOCOL_LABELS: Record<IdentityProviderProtocol, string> = {
  [IdentityProviderProtocol.OIDC]: 'OIDC',
  [IdentityProviderProtocol.SAML]: 'SAML',
};

const identifierOf = (idp?: IdentityProvider) => (idp?.protocol === IdentityProviderProtocol.SAML ? idp.idpEntityId : idp?.clientId) || '';

// One row per supported provider, so providers that are not set up stay visible.
// Only rendered for ADMIN/OWNER; the backend enforces the same.
export const IdentityProvidersTable: React.FC = () => {
  const { data, identityProviders, loading, error, refetch, saveOidc, saveSaml, saving } = useIdentityProvidersCRUD();

  const [editOpenFor, setEditOpenFor] = useState<AuthProviderId | null>(null);
  const [justSavedFor, setJustSavedFor] = useState<AuthProviderId | null>(null);
  // Bumped after a save so the drawer remounts with the stored values as its new baseline.
  const [savedVersion, setSavedVersion] = useState(0);

  const openDrawer = useCallback((providerId: AuthProviderId) => {
    setJustSavedFor(null);
    setEditOpenFor(providerId);
  }, []);

  const byProviderId = useMemo(() => new Map(identityProviders.map((idp) => [idp.providerId, idp])), [identityProviders]);

  const columns: TableProps['columns'] = useMemo(
    () => [
      { key: IdentityProviderTableColumn.Icon, label: '', noSort: true, noFilter: true },
      { key: IdentityProviderTableColumn.Provider, label: STRINGS.PROVIDER, noSort: true, noFilter: true },
      { key: IdentityProviderTableColumn.Protocol, label: STRINGS.PROTOCOL, noSort: true, noFilter: true },
      { key: IdentityProviderTableColumn.Status, label: STRINGS.STATUS, noSort: true, noFilter: true },
      { key: IdentityProviderTableColumn.Identifier, label: STRINGS.IDENTIFIER, noSort: true, noFilter: true, minWidth: 200 },
    ],
    [],
  );

  // Until the first response arrives every provider would read as not set up, so show the loader instead.
  const hasData = !!data;
  const rows: TableProps['rows'] = useMemo(() => {
    if (error || !hasData) return [];
    return AUTH_PROVIDERS.map((provider) => {
      const idp = byProviderId.get(provider.providerId);
      const Icon = provider.icon;
      const identifier = identifierOf(idp);
      const status = !idp ? STRINGS.IDP_NOT_SET_UP : idp.enabled ? STRINGS.IDP_SET_UP : STRINGS.IDP_DISABLED;
      const statusVariant = !idp ? TagVariants.Default : idp.enabled ? TagVariants.Green : TagVariants.Yellow;

      const IconCell = () => (
        <IconCircle>
          <Icon size={20} />
        </IconCircle>
      );
      const ProviderCell = () => (
        <ProviderButton
          type='button'
          aria-label={idp ? STRINGS.EDIT_IDENTITY_PROVIDER(provider.displayName) : STRINGS.CONNECT_IDENTITY_PROVIDER(provider.displayName)}
          onClick={(e) => {
            e.stopPropagation();
            openDrawer(provider.providerId);
          }}
        >
          <Typography size={TypographySize.XS} nowrap>
            {provider.displayName}
          </Typography>
        </ProviderButton>
      );
      const ProtocolCell = () =>
        idp ? (
          <Tag label={PROTOCOL_LABELS[idp.protocol]} />
        ) : (
          <Typography size={TypographySize.XS} color={TypographyColor.Secondary}>
            -
          </Typography>
        );
      const StatusCell = () => <Tag variant={statusVariant} label={status} />;
      const IdentifierCell = () => (
        <IdentifierText size={TypographySize.XS} color={identifier ? undefined : TypographyColor.Secondary} nowrap>
          {identifier || '-'}
        </IdentifierText>
      );

      return {
        id: provider.providerId,
        'data-id': `identity-provider-${provider.providerId}`,
        onClick: () => openDrawer(provider.providerId),
        cells: [
          { key: IdentityProviderTableColumn.Icon, rawValue: '', component: IconCell },
          { key: IdentityProviderTableColumn.Provider, rawValue: provider.displayName, component: ProviderCell },
          { key: IdentityProviderTableColumn.Protocol, rawValue: idp?.protocol ?? '', component: ProtocolCell },
          { key: IdentityProviderTableColumn.Status, rawValue: status, component: StatusCell },
          { key: IdentityProviderTableColumn.Identifier, rawValue: identifier, component: IdentifierCell },
        ],
      };
    });
  }, [byProviderId, error, hasData, openDrawer]);

  const editProvider = AUTH_PROVIDERS.find((p) => p.providerId === editOpenFor);

  const withSaved =
    <T,>(save: (input: T) => Promise<string | undefined>, providerId: AuthProviderId) =>
    async (input: T) => {
      const saveError = await save(input);
      if (!saveError) {
        setJustSavedFor(providerId);
        setSavedVersion((v) => v + 1);
      }
      return saveError;
    };

  return (
    <>
      {!loading && !error && identityProviders.length === 0 && <Note status={StatusType.Default} message={STRINGS.IDP_NONE_CONNECTED} fullWidth />}

      <Table
        variant={TableVariant.Pretty}
        columns={columns}
        rows={rows}
        isLoading={loading}
        activeRowId={editOpenFor}
        noDataProps={
          error
            ? {
                title: STRINGS.IDP_LOAD_ERROR_TITLE,
                subTitle: STRINGS.IDP_LOAD_ERROR_SUBTITLE,
                button: { label: STRINGS.RETRY, onClick: () => refetch() },
              }
            : undefined
        }
      />

      {editProvider && (
        <EditIdentityProviderDrawer
          key={`${editProvider.providerId}-${savedVersion}`}
          provider={editProvider}
          existing={byProviderId.get(editProvider.providerId)}
          saving={saving}
          justSaved={justSavedFor === editProvider.providerId}
          onSaveOidc={withSaved(saveOidc, editProvider.providerId)}
          onSaveSaml={withSaved(saveSaml, editProvider.providerId)}
          onClose={() => setEditOpenFor(null)}
        />
      )}
    </>
  );
};
