export const GET_DESCRIBE_ODIGOS = `
  query DescribeOdigos {
    describeOdigos {
      odigosVersion {
        name
        value
        status
        explain
      }
      kubernetesVersion {
        name
        value
        status
        explain
      }
      tier {
        name
        value
        status
        explain
      }
      installationMethod {
        name
        value
        status
        explain
      }
      numberOfDestinations
      numberOfSources
      clusterCollector {
        enabled {
          name
          value
          status
          explain
        }
        collectorGroup {
          name
          value
          status
          explain
        }
        deployed {
          name
          value
          status
          explain
        }
        deployedError {
          name
          value
          status
          explain
        }
        collectorReady {
          name
          value
          status
          explain
        }
        deploymentCreated {
          name
          value
          status
          explain
        }
        expectedReplicas {
          name
          value
          status
          explain
        }
        healthyReplicas {
          name
          value
          status
          explain
        }
        failedReplicas {
          name
          value
          status
          explain
        }
        failedReplicasReason {
          name
          value
          status
          explain
        }
      }
      nodeCollector {
        enabled {
          name
          value
          status
          explain
        }
        collectorGroup {
          name
          value
          status
          explain
        }
        deployed {
          name
          value
          status
          explain
        }
        deployedError {
          name
          value
          status
          explain
        }
        collectorReady {
          name
          value
          status
          explain
        }
        daemonSet {
          name
          value
          status
          explain
        }
        desiredNodes {
          name
          value
          status
          explain
        }
        currentNodes {
          name
          value
          status
          explain
        }
        updatedNodes {
          name
          value
          status
          explain
        }
        availableNodes {
          name
          value
          status
          explain
        }
      }
    }
  }
`

export const GET_DESCRIBE_SOURCE = `
  query DescribeSource($namespace: String!, $kind: String!, $name: String!) {
    describeSource(namespace: $namespace, kind: $kind, name: $name) {
      totalPods
      podsPhasesCount
      pods {
        podName {
          name
          value
          status
          explain
        }
        nodeName {
          name
          value
          status
          explain
        }
        phase {
          name
          value
          status
          explain
        }
        containers {
          containerName {
            name
            value
            status
            explain
          }
          actualDevices {
            name
            value
            status
            explain
          }
          instrumentationInstances {
            healthy {
              name
              value
              status
              explain
            }
            message {
              name
              value
              status
              explain
            }
            identifyingAttributes {
              name
              value
              status
              explain
            }
          }
        }
      }
    }
  }
`
