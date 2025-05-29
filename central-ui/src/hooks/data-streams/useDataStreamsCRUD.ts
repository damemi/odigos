import { useEffect } from 'react';
import { useProxy, useProxyID } from '../proxy';
import { useConfig } from '../config';
import { STORAGE_KEYS } from '@/utils';
import { useSourceCRUD } from '../sources';
import { useDestinationCRUD } from '../destinations';
import { Crud, type DataStream, StatusType } from '@odigos/ui-kit/types';
import { useDataStreamStore, useNotificationStore } from '@odigos/ui-kit/store';
import { DELETE_DATA_STREAM, GET_DATA_STREAMS, UPDATE_DATA_STREAM } from '@/graphql';
import { DEFAULT_DATA_STREAM_NAME, DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';

interface UseDataStreamsCrud {
  dataStreamsLoading: boolean;
  dataStreams: DataStream[];
  selectedStreamName: string;
  fetchDataStreams: () => Promise<void>;
  updateDataStream: (dataStreamName: string, dataStream: DataStream) => Promise<void>;
  deleteDataStream: (dataStreamName: string) => Promise<void>;
}

export const useDataStreamsCRUD = (): UseDataStreamsCrud => {
  const { isReadonly } = useConfig();
  const { proxyID } = useProxyID();
  const { fetchSourcesPaginated } = useSourceCRUD();
  const { fetchDestinations } = useDestinationCRUD();
  const { addNotification } = useNotificationStore();
  const { dataStreams, setDataStreams, addDataStreams, removeDataStreams, dataStreamsLoading, setDataStreamsLoading, selectedStreamName, setSelectedStreamName } = useDataStreamStore();

  const notifyUser = (type: StatusType, title: string, message: string, id?: string, hideFromHistory?: boolean) => {
    addNotification({ type, title, message, hideFromHistory });
  };

  const { fetch: fetchDataStreamsQuery } = useProxy<{ computePlatform: { dataStreams?: DataStream[] } }>(GET_DATA_STREAMS);
  const { fetch: mutateUpdate } = useProxy<{ updateDataStream: { name: string } }, { id: string; dataStream: DataStream }>(UPDATE_DATA_STREAM);
  const { fetch: mutateDelete } = useProxy<{ deleteDataStream: boolean }, { id: string }>(DELETE_DATA_STREAM);

  const fetchDataStreams = async (overrideStreamNameSelection?: string) => {
    setDataStreamsLoading(true);
    const { data } = await fetchDataStreamsQuery();
    setDataStreamsLoading(false);

    if (data?.computePlatform?.dataStreams) {
      setDataStreams(data.computePlatform.dataStreams);

      if (overrideStreamNameSelection) {
        setSelectedStreamName(overrideStreamNameSelection);
      } else {
        const streamNameFromStorage = sessionStorage.getItem(STORAGE_KEYS.SELECTED_DATA_STREAM(proxyID));
        const storedSteamNameExistsInCP = data.computePlatform.dataStreams.some((stream) => stream.name === streamNameFromStorage);

        if (streamNameFromStorage && storedSteamNameExistsInCP) {
          setSelectedStreamName(streamNameFromStorage);
        } else {
          setSelectedStreamName(DEFAULT_DATA_STREAM_NAME);
        }
      }
    }
  };

  const updateDataStream: UseDataStreamsCrud['updateDataStream'] = async (dataStreamName, dataStream) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Updating data stream...', undefined, true);
      const { data } = await mutateUpdate({ id: dataStreamName, dataStream });

      if (data) {
        const oldStream = dataStreams.find((x) => x.name === dataStreamName);
        if (oldStream) removeDataStreams([oldStream]);
        const newStream = data.updateDataStream;
        addDataStreams([newStream]);
        notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${oldStream?.name}" data stream`);

        const switchToStream = selectedStreamName === oldStream?.name ? newStream.name : selectedStreamName;
        await fetchDataStreams(switchToStream);
        await fetchSourcesPaginated();
        await fetchDestinations();
      }
    }
  };

  const deleteDataStream: UseDataStreamsCrud['deleteDataStream'] = async (dataStreamName) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Deleting data stream...', undefined, true);
      const { data } = await mutateDelete({ id: dataStreamName });

      if (data) {
        const oldStream = dataStreams.find((x) => x.name === dataStreamName);
        if (oldStream) removeDataStreams([oldStream]);
        notifyUser(StatusType.Success, Crud.Delete, `Successfully deleted "${oldStream?.name}" data stream`);

        const switchToStream = selectedStreamName === oldStream?.name ? DEFAULT_DATA_STREAM_NAME : selectedStreamName;
        await fetchDataStreams(switchToStream);
        await fetchSourcesPaginated();
        await fetchDestinations();
      }
    }
  };

  useEffect(() => {
    if (selectedStreamName) sessionStorage.setItem(STORAGE_KEYS.SELECTED_DATA_STREAM(proxyID), selectedStreamName);
  }, [selectedStreamName, proxyID]);

  useEffect(() => {
    if (!dataStreams.length && !dataStreamsLoading) fetchDataStreams();
  }, []);

  return {
    dataStreamsLoading,
    dataStreams,
    selectedStreamName,
    fetchDataStreams,
    updateDataStream,
    deleteDataStream,
  };
};
