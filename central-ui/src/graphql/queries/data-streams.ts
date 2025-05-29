export const GET_DATA_STREAMS = `
  query GetDataStreams {
    computePlatform {
      dataStreams {
        name
      }
    }
  }
`;
