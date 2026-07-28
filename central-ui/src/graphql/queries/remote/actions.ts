import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// Dynamic action-type catalog (YAML-driven) introduced in odigos v1.31 (the
// `actionTypes` resolver is unreleased as of v1.30.x). Proxies older than that
// have no bucket here -> `getVersionedQuery` returns undefined -> `canRun` is
// false and the kit falls back to its hard-coded ACTION_OPTIONS.
// `ActionTypeOption.category` + `subtitle` (picker grouping and one-line menu
// copy) landed with the action-menu redesign. Older proxies reject the selection
// set, so keep them off until v1.33+; the kit falls back to
// `ACTION_TYPE_TO_CATEGORY` and uses `description` for the menu without them.
const ACTION_TYPES_QUERY = (withCategoryAndSubtitle = false) => `
  query GetActionTypes {
    actionTypes {
      type
      displayName
      ${withCategoryAndSubtitle ? 'category\n      subtitle' : ''}
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
  }`;

export const GET_ACTION_TYPES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.33': ACTION_TYPES_QUERY(true),
    'v1.31': ACTION_TYPES_QUERY(),
  },
  // VM proxies resolve the action catalog from the versioned vmagent
  // `actionTypes` dialect document (see the VmSpec on GET_ACTION_TYPES in
  // odigos-api-adapter).
};

export const GET_ACTIONS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // customFormatMaskings / customRegexMaskings on PiiMasking landed in odigos
    // v1.33 (CORE-1372). Older proxies reject that selection set.
    'v1.33': `
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
            customFormatMaskings {
              lookupKey
              dataFormat
            }
            customRegexMaskings {
              regex
            }
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
