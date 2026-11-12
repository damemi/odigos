'use client';

import React, { useState, type FC } from 'react';
import { DiagnoseIcon } from '@odigos/ui-kit/icons';
import { useOdigosApi } from '@odigos/ui-kit/contexts';
import { StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { Button, ButtonVariants, Drawer, FlexColumn, Typography, TypographyColor, TypographySize } from '@odigos/ui-kit/components';

const DEFAULT_VM_DIAGNOSE_INPUT = {
  includeProfiles: true,
  includeMetrics: true,
  includeSourceWorkloads: false,
  sourceWorkloadNamespaces: [] as string[],
};

export interface VmDiagnoseDrawerProps {
  isOpen: boolean;
  onClose: () => void;
}

/** VM-only system diagnose: one download action, no k8s content/namespace options. */
export const VmDiagnoseDrawer: FC<VmDiagnoseDrawerProps> = ({ isOpen, onClose }) => {
  const { describeApi } = useOdigosApi();
  const { addNotification } = useNotificationStore();
  const [busy, setBusy] = useState(false);

  const onDownload = async () => {
    if (!describeApi.downloadDiagnose || busy) return;
    setBusy(true);
    try {
      const { data, error } = await describeApi.downloadDiagnose(DEFAULT_VM_DIAGNOSE_INPUT);
      if (error) {
        addNotification({ type: StatusType.Error, title: 'Diagnose failed', message: error });
        return;
      }
      const size = data?.stats?.totalSizeHuman;
      addNotification({
        type: StatusType.Success,
        title: 'Diagnose',
        message: size ? `Diagnose archive downloaded (${size})` : 'Diagnose archive downloaded',
      });
      onClose();
    } catch (err) {
      addNotification({
        type: StatusType.Error,
        title: 'Diagnose failed',
        message: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Drawer
      isOpen={isOpen}
      header={{
        onClose,
        icon: DiagnoseIcon,
        title: 'Diagnose',
      }}
    >
      <FlexColumn $gap={24}>
        <Typography size={TypographySize.XXS} color={TypographyColor.Secondary}>
          Download a tar.gz archive with diagnostics from this VM agent, and share it with the Odigos support team for
          troubleshooting.
        </Typography>
        <Button
          label={busy ? 'Collecting…' : 'Download Diagnose'}
          variant={ButtonVariants.Primary}
          rightIcon={DiagnoseIcon}
          onClick={onDownload}
          disabled={busy || !describeApi.downloadDiagnose}
          loading={busy}
        />
      </FlexColumn>
    </Drawer>
  );
};
