export const GET_METRICS = `
  query GetOverviewMetrics {
    getOverviewMetrics {
      sources {
        namespace
        kind
        name
        totalDataSent
        throughput
      }
      destinations {
        id
        totalDataSent
        throughput
      }
    }
  }
`
