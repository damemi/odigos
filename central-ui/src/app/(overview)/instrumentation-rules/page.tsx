'use client';

import React from 'react';
import { useDataStreamsCRUD } from '@/hooks';
import { useModalStore } from '@odigos/ui-kit/store';
import { OverviewModalsAndDrawers } from '@/components';
import { TABLE_MAX_HEIGHT, TABLE_MAX_WIDTH } from '@/utils';
import { EntityTypes, OtherEntityTypes } from '@odigos/ui-kit/types';
import { DataFlowActionsMenu, InstrumentationRuleTable } from '@odigos/ui-kit/containers';

export default function Page() {
  const { setCurrentModal } = useModalStore();
  const { updateDataStream, deleteDataStream } = useDataStreamsCRUD();

  return (
    <>
      <DataFlowActionsMenu
        addEntity={EntityTypes.InstrumentationRule}
        onClickNewDataStream={() => setCurrentModal(OtherEntityTypes.DataStream)}
        updateDataStream={updateDataStream}
        deleteDataStream={deleteDataStream}
      />
      <InstrumentationRuleTable maxHeight={TABLE_MAX_HEIGHT} maxWidth={TABLE_MAX_WIDTH} />
      <OverviewModalsAndDrawers />
    </>
  );
}
