'use client';

import React from 'react';
import { useModalStore } from '@odigos/ui-kit/store';
import { OverviewModalsAndDrawers } from '@/components';
import { useDataStreamsCRUD, useMetrics } from '@/hooks';
import { TABLE_MAX_HEIGHT, TABLE_MAX_WIDTH } from '@/utils';
import { EntityTypes, OtherEntityTypes } from '@odigos/ui-kit/types';
import { DataFlowActionsMenu, DestinationTable } from '@odigos/ui-kit/containers';

export default function Page() {
  const { metrics } = useMetrics();
  const { setCurrentModal } = useModalStore();
  const { updateDataStream, deleteDataStream } = useDataStreamsCRUD();

  return (
    <>
      <DataFlowActionsMenu
        addEntity={EntityTypes.Destination}
        onClickNewDataStream={() => setCurrentModal(OtherEntityTypes.DataStream)}
        updateDataStream={updateDataStream}
        deleteDataStream={deleteDataStream}
      />
      <DestinationTable metrics={metrics} maxHeight={TABLE_MAX_HEIGHT} maxWidth={TABLE_MAX_WIDTH} />
      <OverviewModalsAndDrawers />
    </>
  );
}
