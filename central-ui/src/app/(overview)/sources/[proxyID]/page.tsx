'use client';

import React from 'react';
import { useModalStore } from '@odigos/ui-kit/store';
import { OverviewModalsAndDrawers } from '@/components';
import { TABLE_MAX_HEIGHT, TABLE_MAX_WIDTH } from '@/utils';
import { EntityTypes, OtherEntityTypes } from '@odigos/ui-kit/types';
import { useDataStreamsCRUD, useMetrics, useSourceCRUD, useWorkloadUtils } from '@/hooks';
import { DataFlowActionsMenu, MultiSourceControl, SourceTable } from '@odigos/ui-kit/containers';

export default function Page() {
  const { metrics } = useMetrics();
  const { setCurrentModal } = useModalStore();
  const { restartWorkloads } = useWorkloadUtils();
  const { sources, persistSources } = useSourceCRUD();
  const { updateDataStream, deleteDataStream } = useDataStreamsCRUD();

  return (
    <>
      <DataFlowActionsMenu
        addEntity={EntityTypes.Source}
        onClickNewDataStream={() => setCurrentModal(OtherEntityTypes.DataStream)}
        updateDataStream={updateDataStream}
        deleteDataStream={deleteDataStream}
      />
      <SourceTable metrics={metrics} maxHeight={TABLE_MAX_HEIGHT} maxWidth={TABLE_MAX_WIDTH} />
      <MultiSourceControl totalSourceCount={sources.length} uninstrumentSources={(payload) => persistSources(payload, {})} restartWorkloads={restartWorkloads} />
      <OverviewModalsAndDrawers />
    </>
  );
}
