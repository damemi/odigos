import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// Dynamic action-type catalog (YAML-driven) introduced in odigos v1.31 (the
// `actionTypes` resolver is unreleased as of v1.30.x). Proxies older than that
// have no bucket here -> `getVersionedQuery` returns undefined -> `canRun` is
// false and the kit falls back to its hard-coded ACTION_OPTIONS.
export const GET_ACTION_TYPES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.31': `
  query GetActionTypes {
    actionTypes {
      type
      displayName
      description
      allowedSignals
      docsUrl
      fields {
        name
        displayName
        componentType
        componentProperties
        initialValue
        renderCondition
      }
    }
  }`,
  },
  // VM proxies resolve the action catalog from the versioned vmagent
  // `actionTypes` dialect document (see the VmSpec on GET_ACTION_TYPES in
  // odigos-api-adapter).
};

export const GET_ACTIONS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.29': `
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
            extractAttribute {
              extractions {
                targetAttributeName
                lookupKey
                dataFormat
                regex
              }
            }
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
