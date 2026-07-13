import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// ---- Noisy Operations ----

export const CREATE_NOISY_OPERATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `sourceScopes { sources/namespaces/languages }` shape is v1.26+ (older
    // proxies expose the legacy SourcesScope shape). Gate at v1.26.
    'v1.26': `
  mutation CreateNoisyOperationRule($samplingId: ID!, $rule: NoisyOperationRuleInput!) {
    createNoisyOperationRule(samplingId: $samplingId, rule: $rule) {
      ruleId
      name
      disabled
      sourceScopes {
        sources { namespace kind name }
        namespaces
        languages
      }
      operation {
        httpServer { route routePrefix method }
        httpClient { serverAddress templatedPath templatedPathPrefix method }
      }
      percentageAtMost
      notes
    }
  }`,
    // HTTP queryParams + gRPC head-sampling matchers landed in odigos v1.32
    // (after v1.31.2). Older proxies reject that selection set.
    'v1.32': `
  mutation CreateNoisyOperationRule($samplingId: ID!, $rule: NoisyOperationRuleInput!) {
    createNoisyOperationRule(samplingId: $samplingId, rule: $rule) {
      ruleId
      name
      disabled
      sourceScopes {
        sources { namespace kind name }
        namespaces
        languages
      }
      operation {
        httpServer { route routePrefix method queryParams { name valueExact } }
        httpClient { serverAddress templatedPath templatedPathPrefix method }
        grpcServer { method service }
        grpcClient { method service serverAddress }
      }
      percentageAtMost
      notes
    }
  }`,
  },
};

export const UPDATE_NOISY_OPERATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `sourceScopes` selection is v1.26+ (see CreateNoisyOperationRule).
    'v1.26': `
  mutation UpdateNoisyOperationRule($samplingId: ID!, $ruleId: ID!, $rule: NoisyOperationRuleInput!) {
    updateNoisyOperationRule(samplingId: $samplingId, ruleId: $ruleId, rule: $rule) {
      ruleId
      name
      disabled
      sourceScopes {
        sources { namespace kind name }
        namespaces
        languages
      }
      operation {
        httpServer { route routePrefix method }
        httpClient { serverAddress templatedPath templatedPathPrefix method }
      }
      percentageAtMost
      notes
    }
  }`,
    // HTTP queryParams + gRPC head-sampling matchers landed in odigos v1.32
    // (after v1.31.2). Older proxies reject that selection set.
    'v1.32': `
  mutation UpdateNoisyOperationRule($samplingId: ID!, $ruleId: ID!, $rule: NoisyOperationRuleInput!) {
    updateNoisyOperationRule(samplingId: $samplingId, ruleId: $ruleId, rule: $rule) {
      ruleId
      name
      disabled
      sourceScopes {
        sources { namespace kind name }
        namespaces
        languages
      }
      operation {
        httpServer { route routePrefix method queryParams { name valueExact } }
        httpClient { serverAddress templatedPath templatedPathPrefix method }
        grpcServer { method service }
        grpcClient { method service serverAddress }
      }
      percentageAtMost
      notes
    }
  }`,
  },
};

export const DELETE_NOISY_OPERATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.24': `
  mutation DeleteNoisyOperationRule($samplingId: ID!, $ruleId: ID!) {
    deleteNoisyOperationRule(samplingId: $samplingId, ruleId: $ruleId)
  }`,
  },
};

// ---- Highly Relevant Operations ----

export const CREATE_HIGHLY_RELEVANT_OPERATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `sourceScopes` selection is v1.26+ (see CreateNoisyOperationRule).
    'v1.26': `
  mutation CreateHighlyRelevantOperationRule($samplingId: ID!, $rule: HighlyRelevantOperationRuleInput!) {
    createHighlyRelevantOperationRule(samplingId: $samplingId, rule: $rule) {
      ruleId
      name
      disabled
      sourceScopes {
        sources { namespace kind name }
        namespaces
        languages
      }
      error
      durationAtLeastMs
      operation {
        httpServer { route routePrefix method }
        kafkaConsumer { kafkaTopic }
        kafkaProducer { kafkaTopic }
      }
      percentageAtLeast
      notes
    }
  }`,
  },
};

export const UPDATE_HIGHLY_RELEVANT_OPERATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `sourceScopes` selection is v1.26+ (see CreateNoisyOperationRule).
    'v1.26': `
  mutation UpdateHighlyRelevantOperationRule($samplingId: ID!, $ruleId: ID!, $rule: HighlyRelevantOperationRuleInput!) {
    updateHighlyRelevantOperationRule(samplingId: $samplingId, ruleId: $ruleId, rule: $rule) {
      ruleId
      name
      disabled
      sourceScopes {
        sources { namespace kind name }
        namespaces
        languages
      }
      error
      durationAtLeastMs
      operation {
        httpServer { route routePrefix method }
        kafkaConsumer { kafkaTopic }
        kafkaProducer { kafkaTopic }
      }
      percentageAtLeast
      notes
    }
  }`,
  },
};

export const DELETE_HIGHLY_RELEVANT_OPERATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.24': `
  mutation DeleteHighlyRelevantOperationRule($samplingId: ID!, $ruleId: ID!) {
    deleteHighlyRelevantOperationRule(samplingId: $samplingId, ruleId: $ruleId)
  }`,
  },
};

// ---- Cost Reduction Rules ----

export const CREATE_COST_REDUCTION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `sourceScopes` selection is v1.26+ (see CreateNoisyOperationRule).
    'v1.26': `
  mutation CreateCostReductionRule($samplingId: ID!, $rule: CostReductionRuleInput!) {
    createCostReductionRule(samplingId: $samplingId, rule: $rule) {
      ruleId
      name
      disabled
      sourceScopes {
        sources { namespace kind name }
        namespaces
        languages
      }
      operation {
        httpServer { route routePrefix method }
        kafkaConsumer { kafkaTopic }
        kafkaProducer { kafkaTopic }
      }
      percentageAtMost
      notes
    }
  }`,
  },
};

export const UPDATE_COST_REDUCTION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `sourceScopes` selection is v1.26+ (see CreateNoisyOperationRule).
    'v1.26': `
  mutation UpdateCostReductionRule($samplingId: ID!, $ruleId: ID!, $rule: CostReductionRuleInput!) {
    updateCostReductionRule(samplingId: $samplingId, ruleId: $ruleId, rule: $rule) {
      ruleId
      name
      disabled
      sourceScopes {
        sources { namespace kind name }
        namespaces
        languages
      }
      operation {
        httpServer { route routePrefix method }
        kafkaConsumer { kafkaTopic }
        kafkaProducer { kafkaTopic }
      }
      percentageAtMost
      notes
    }
  }`,
  },
};

export const DELETE_COST_REDUCTION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.24': `
  mutation DeleteCostReductionRule($samplingId: ID!, $ruleId: ID!) {
    deleteCostReductionRule(samplingId: $samplingId, ruleId: $ruleId)
  }`,
  },
};

// ---- Sampling Config ----

export const UPDATE_LOCAL_UI_SAMPLING_CONFIG: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.24': `
  mutation UpdateLocalUiSamplingConfig($config: SamplingConfigInput) {
    updateLocalUiSamplingConfig(config: $config)
  }`,
  },
};
