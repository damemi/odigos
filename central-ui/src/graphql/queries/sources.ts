export const GET_SOURCES = `
  query GetSources($nextPage: String!) {
    computePlatform {
      sources(nextPage: $nextPage) {
        nextPage
        items {
          namespace
          name
          kind
          dataStreamNames
          selected
          otelServiceName
          containers {
            containerName
            language
            runtimeVersion
            instrumented
            instrumentationMessage
            otelDistroName
          }
          conditions {
            status
            type
            reason
            message
            lastTransitionTime
          }
        }
      }
    }
  }
`;

export const GET_SOURCE = `
  query GetSource($sourceId: K8sSourceId!) {
    computePlatform {
      source(sourceId: $sourceId) {
        namespace
        name
        kind
        dataStreamNames
        selected
        otelServiceName
        containers {
          containerName
          language
          runtimeVersion
          instrumented
          instrumentationMessage
          otelDistroName
        }
        conditions {
          status
          type
          reason
          message
          lastTransitionTime
        }
      }
    }
  }
`;

export const GET_SOURCE_CONDITIONS = `
  query GetSourceConditions {
    sourceConditions {
      namespace
      name
      kind
      conditions {
        status
        type
        reason
        message
        lastTransitionTime
      }
    }
  }
`;
