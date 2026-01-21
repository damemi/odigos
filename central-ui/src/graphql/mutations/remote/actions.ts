import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const CREATE_ACTION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
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
        lastTransitionTime
      }
    }
  }`,
  },
};

export const UPDATE_ACTION: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
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
        lastTransitionTime
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
