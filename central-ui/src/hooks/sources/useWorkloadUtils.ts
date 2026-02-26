import { useConfig } from '../config';
import { useRemoteFetch } from '../proxy';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { RECOVER_FROM_ROLLBACK, RESTART_POD, RESTART_WORKLOADS } from '@/graphql';
import { type WorkloadId, EntityTypes, StatusType, Crud } from '@odigos/ui-kit/types';

interface UseWorkloadUtils {
  restartWorkloads: (sourceIds: WorkloadId[]) => Promise<void>;
  restartPod: (namespace: string, name: string) => Promise<void>;
  recoverFromRollback: (sourceId: WorkloadId) => Promise<void>;
}

export const useWorkloadUtils = (): UseWorkloadUtils => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();

  const notifyUser = (type: StatusType, title: string, message: string, id?: WorkloadId, hideFromHistory?: boolean) => {
    addNotification({ type, title, message, crdType: EntityTypes.Source, target: id ? getSseTargetFromId(id, EntityTypes.Source) : undefined, hideFromHistory });
  };

  const { fetch: mutateRestartWorkloads } = useRemoteFetch<{ restartWorkloads: boolean }, { sourceIds: WorkloadId[] }>(RESTART_WORKLOADS);
  const { fetch: mutateRestartPod } = useRemoteFetch<{ restartPod: boolean }, { namespace: string; name: string }>(RESTART_POD);
  const { fetch: mutateRecoverFromRollback } = useRemoteFetch<{ recoverFromRollbackForWorkload: boolean }, { sourceId: WorkloadId }>(RECOVER_FROM_ROLLBACK);

  const restartWorkloads: UseWorkloadUtils['restartWorkloads'] = async (sourceIds) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Restarting sources...', undefined, true);

      const { data } = await mutateRestartWorkloads({ sourceIds });
      if (data?.restartWorkloads) notifyUser(StatusType.Success, Crud.Update, `Successfully restarted ${sourceIds.length} sources`);
    }
  };

  const restartPod: UseWorkloadUtils['restartPod'] = async (namespace, name) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Restarting pod...', undefined, true);

      const { data } = await mutateRestartPod({ namespace, name });
      if (data?.restartPod) notifyUser(StatusType.Success, Crud.Update, `Successfully restarted pod ${namespace}/${name}`);
    }
  };

  const recoverFromRollback: UseWorkloadUtils['recoverFromRollback'] = async (sourceId) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Recovering from rollback...', undefined, true);

      const { data } = await mutateRecoverFromRollback({ sourceId });
      if (data?.recoverFromRollbackForWorkload) notifyUser(StatusType.Success, Crud.Update, 'Successfully triggered recovery from rollback');
    }
  };

  return {
    restartWorkloads,
    restartPod,
    recoverFromRollback,
  };
};
