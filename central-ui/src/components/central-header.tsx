'use client';

import React, { useState } from 'react';
import { useUserStore } from '@/store';
import { ROLE_LABELS } from '@/utils';
import { useTheme } from 'styled-components';
import { StatusType } from '@odigos/ui-kit/types';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { useAuth, useConfig, useHealth, useProxy } from '@/hooks';
import { VmDiagnoseDrawer } from '@/components/vm-diagnose-drawer';
import { StatusKeys, useStatusStore, type StatusValues } from '../store';
import { ExclamationTriangleIcon, TerminalIcon } from '@odigos/ui-kit/icons';
import { AppHeader, type AppHeaderBadge } from '@odigos/ui-kit/containers';
import { ButtonVariants, IconButton, WarningModal } from '@odigos/ui-kit/components';

const toBadge = (id: string, value?: StatusValues): AppHeaderBadge[] => (value ? [{ id, label: value.label, status: value.status, leftIcon: value.leftIcon, tooltip: value.tooltip }] : []);

// The kit's `AppHeader` with Central's data and the signed-in user for its account menu. Central's own health shows
// everywhere; the cluster's name, its statuses and its token show only inside a cluster (the token status of the last
// cluster stays in the store).
export const CentralHeader = () => {
  useHealth();

  const theme = useTheme();
  const { isVm } = useOdigos();
  const { user } = useUserStore();
  const { isReadonly } = useConfig();
  const { proxyID, proxyName } = useProxy();
  const { systemConfig, signOut } = useAuth();
  const [isVmDiagnoseOpen, setIsVmDiagnoseOpen] = useState(false);

  const backendStatus = useStatusStore((state) => state[StatusKeys.Backend]);
  const instrumentationStatus = useStatusStore((state) => state[StatusKeys.Instrumentation]);
  const tokenStatus = useStatusStore((state) => (proxyID ? state[StatusKeys.Token] : undefined));
  const dismissTokenModal = () => useStatusStore.getState().dismissModal(StatusKeys.Token);

  return (
    <>
      <AppHeader
        central
        isReadonly={isReadonly}
        contextBadges={proxyID && proxyName ? [{ id: 'proxy-name', label: proxyName, status: StatusType.Default }] : []}
        statusBadges={[...toBadge('backend-status', backendStatus), ...(proxyID ? toBadge('instrumentation-status', instrumentationStatus) : [])]}
        rightBadges={toBadge('token-status', tokenStatus)}
        // The kit's System Overview drawer is Kubernetes-only; VM agents get their own diagnose drawer instead.
        showSystemDrawer={!!proxyID && !isVm}
        extraRight={!!proxyID && isVm && <IconButton icon={TerminalIcon} onClick={() => setIsVmDiagnoseOpen((prev) => !prev)} />}
        account={{ email: user?.email, role: user?.role && ROLE_LABELS[user.role], version: systemConfig?.version, onSignOut: () => signOut() }}
      />
      {isVm && <VmDiagnoseDrawer isOpen={isVmDiagnoseOpen} onClose={() => setIsVmDiagnoseOpen(false)} />}

      {tokenStatus && !tokenStatus.modalDismissed && (
        <WarningModal
          isOpen
          onClose={dismissTokenModal}
          visual={() => <ExclamationTriangleIcon size={50} fill={tokenStatus.status === StatusType.Warning ? theme.v2.colors.yellow[500] : theme.v2.colors.red[500]} />}
          title={tokenStatus.status === StatusType.Warning ? 'Token about to expire' : 'Token expired'}
          description={tokenStatus.label?.toString() || ''}
          denyButton={{
            label: 'Dismiss',
            variant: ButtonVariants.Secondary,
            onClick: dismissTokenModal,
          }}
        />
      )}
    </>
  );
};
