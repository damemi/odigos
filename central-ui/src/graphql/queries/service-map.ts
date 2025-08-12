export const GET_SERVICE_MAP = `
  query GetServiceMap {
    getServiceMap {
      services {
        serviceName
        services {
          serviceName
          requests
          dateTime
        }
      }
    }
  }
`;
