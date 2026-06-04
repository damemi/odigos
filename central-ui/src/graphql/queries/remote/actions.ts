import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_ACTIONS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.28': `
    query GetActions {
      computePlatform {
        actions {
          id
          type
          name
          notes
          disabled
          signals
          fields {
            collectContainerAttributes
            collectReplicaSetAttributes
            collectWorkloadId
            collectClusterId
            labelsAttributes {
              labelKey
              attributeKey
              from
              fromSources
            }
            annotationsAttributes {
              annotationKey
              attributeKey
              from
              fromSources
            }
            clusterAttributes {
              attributeName
              attributeStringValue
            }
            overwriteExistingValues
            attributeNamesToDelete
            renames
            piiCategories
            urlTemplatizationRulesGroups {
              filterK8sNamespace
              filterK8sWorkloadKind
              filterK8sWorkloadName
              filterProgrammingLanguage
              notes
              workloadFilters {
                kind
                name
              }
              templatizationRules {
                template
                notes
                examples
              }
            }
          }
          conditions {
            status
            type
            reason
            message
          }
        }
      }
    }`,
    'v1.15': `
  query GetActions {
    computePlatform {
      actions {
        id
        type
        name
        notes
        disabled
        signals
        fields {
          collectContainerAttributes
          collectReplicaSetAttributes
          collectWorkloadId
          collectClusterId
          labelsAttributes {
            labelKey
            attributeKey
            from
            fromSources
          }
          annotationsAttributes {
            annotationKey
            attributeKey
            from
            fromSources
          }
          clusterAttributes {
            attributeName
            attributeStringValue
          }
          overwriteExistingValues
          attributeNamesToDelete
          renames
          piiCategories
          samplingPercentage
          fallbackSamplingRatio
          endpointsFilters {
            httpRoute
            serviceName
            minimumLatencyThreshold
            fallbackSamplingRatio
          }
          servicesNameFilters {
            serviceName
            samplingRatio
            fallbackSamplingRatio
          }
          attributeFilters {
            serviceName
            attributeKey
            fallbackSamplingRatio
            condition {
              stringCondition {
                operation
                expectedValue
              }
            }
          }
        }
        conditions {
          status
          type
          reason
          message

        }
      }
    }
  }`,
    'v1.11': `
  query GetActions {
    computePlatform {
      actions {
        id
        type
        name
        notes
        disabled
        signals
        fields {
          collectContainerAttributes
          collectReplicaSetAttributes
          collectWorkloadId
          collectClusterId
          labelsAttributes {
            labelKey
            attributeKey
          }
          annotationsAttributes {
            annotationKey
            attributeKey
          }
          clusterAttributes {
            attributeName
            attributeStringValue
          }
          overwriteExistingValues
          attributeNamesToDelete
          renames
          piiCategories
          samplingPercentage
          fallbackSamplingRatio
          endpointsFilters {
            httpRoute
            serviceName
            minimumLatencyThreshold
            fallbackSamplingRatio
          }
          servicesNameFilters {
            serviceName
            samplingRatio
            fallbackSamplingRatio
          }
          attributeFilters {
            serviceName
            attributeKey
            fallbackSamplingRatio
            condition {
              stringCondition {
                operation
                expectedValue
              }
            }
          }
        }
        conditions {
          status
          type
          reason
          message

        }
      }
    }
  }`,
  },
};
