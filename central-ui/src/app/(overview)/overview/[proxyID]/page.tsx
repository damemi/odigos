'use client';

import React from 'react';
import { useModalStore } from '@odigos/ui-kit/store';
import { OtherEntityTypes } from '@odigos/ui-kit/types';
import { OverviewModalsAndDrawers } from '@/components';
import { HEADER_HEIGHT, MENU_BAR_HEIGHT } from '@/utils';
import { useDataStreamsCRUD, useMetrics, useSourceCRUD, useWorkloadUtils } from '@/hooks';
import { DataFlow, DataFlowActionsMenu, MultiSourceControl } from '@odigos/ui-kit/containers';

export default function Page() {
  const { metrics } = useMetrics();
  const { setCurrentModal } = useModalStore();
  const { restartWorkloads } = useWorkloadUtils();
  const { sources, persistSources } = useSourceCRUD();
  const { updateDataStream, deleteDataStream } = useDataStreamsCRUD();

  return (
    <>
      <DataFlowActionsMenu onClickNewDataStream={() => setCurrentModal(OtherEntityTypes.DataStream)} updateDataStream={updateDataStream} deleteDataStream={deleteDataStream} />
      <DataFlow height={`calc(100vh - ${HEADER_HEIGHT + MENU_BAR_HEIGHT + 100}px)`} metrics={metrics} />
      <MultiSourceControl totalSourceCount={sources.length} uninstrumentSources={(payload) => persistSources(payload, {})} restartWorkloads={restartWorkloads} />
      <OverviewModalsAndDrawers />
    </>
  );
}
