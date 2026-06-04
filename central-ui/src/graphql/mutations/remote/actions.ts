import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const CREATE_ACTION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.28': `
  mutation CreateAction($action: ActionInput!) {
    createAction(action: $action) {
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
  }`,
    'v1.15': `
  mutation CreateAction($action: ActionInput!) {
    createAction(action: $action) {
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
  }`,
    'v1.11': `
  mutation CreateAction($action: ActionInput!) {
    createAction(action: $action) {
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
  }`,
  },
};

export const UPDATE_ACTION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.28': `
  mutation UpdateAction($id: ID!, $action: ActionInput!) {
    updateAction(id: $id, action: $action) {
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
  }`,
    'v1.15': `
  mutation UpdateAction($id: ID!, $action: ActionInput!) {
    updateAction(id: $id, action: $action) {
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
  }`,
    'v1.11': `
  mutation UpdateAction($id: ID!, $action: ActionInput!) {
    updateAction(id: $id, action: $action) {
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
  }`,
  },
};

export const DELETE_ACTION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation DeleteAction($id: ID!, $actionType: String!) {
    deleteAction(id: $id, actionType: $actionType)
  }`,
  },
};
