export const GET_SOURCES = `
  query GetSources($nextPage: String!) {
    computePlatform {
      sources(nextPage: $nextPage) {
        nextPage
        items {
          namespace
          name
          kind
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
`

export const GET_SOURCE = `
  query GetSource($sourceId: K8sSourceId!) {
    computePlatform {
      source(sourceId: $sourceId) {
        namespace
        name
        kind
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
`

export const GET_INSTRUMENTATION_INSTANCES_HEALTH = `
  query GetInstrumentationInstancesHealth {
    instrumentationInstancesHealth {
      namespace
      name
      kind
      condition {
        status
        type
        reason
        message
        lastTransitionTime
      }
    }
  }
`
