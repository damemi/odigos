import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// Selection set for the InstrumentationRule's row-per-criterion `sourcesScopes`.
// Mirrors the GraphQL schema; callers adapt to the kit's spec-shape via
// `adaptInstrumentationRuleFromWire` / `adaptInstrumentationRuleInputForWire`.
const SOURCES_SCOPE_FIELDS = `
  workloadName
  workloadKind
  workloadNamespace
  workloadLanguage
`;

export const CREATE_INSTRUMENTATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `managedBy` (odigos.io/managed-by owner) introduced in odigos v1.38.0-pre5.
    'v1.38': `
  mutation CreateInstrumentationRule($instrumentationRule: InstrumentationRuleInput!) {
    createInstrumentationRule(instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      managedBy
      sourcesScopes { ${SOURCES_SCOPE_FIELDS} }
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
      customInstrumentations {
        golang {
          packageName
          functionName
          receiverName
          receiverMethodName
        }
        java {
          methodName
          className
        }
        php {
          className
          functionName
        }
      }
    }
  }`,
    'v1.33': `
  mutation CreateInstrumentationRule($instrumentationRule: InstrumentationRuleInput!) {
    createInstrumentationRule(instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      sourcesScopes { ${SOURCES_SCOPE_FIELDS} }
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
      customInstrumentations {
        golang {
          packageName
          functionName
          receiverName
          receiverMethodName
        }
        java {
          methodName
          className
        }
        php {
          className
          functionName
        }
      }
    }
  }`,
    // Starts at v1.26, where `sourcesScopes` replaced `workloads` (was keyed 'v1.28').
    'v1.26': `
  mutation CreateInstrumentationRule($instrumentationRule: InstrumentationRuleInput!) {
    createInstrumentationRule(instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      sourcesScopes { ${SOURCES_SCOPE_FIELDS} }
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
      customInstrumentations {
        golang {
          packageName
          functionName
          receiverName
          receiverMethodName
        }
        java {
          methodName
          className
        }
      }
    }
  }`,
    'v1.11': `
  mutation CreateInstrumentationRule($instrumentationRule: InstrumentationRuleInput!) {
    createInstrumentationRule(instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      # Echoed back so a later edit doesn't clear scope (update is a full replace).
      workloads {
        namespace
        kind
        name
      }
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
      customInstrumentations {
        golang {
          packageName
          functionName
          receiverName
          receiverMethodName
        }
        java {
          methodName
          className
        }
      }
    }
  }`,
  },
};

export const UPDATE_INSTURMENTATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `managedBy` (odigos.io/managed-by owner) introduced in odigos v1.38.0-pre5.
    'v1.38': `
  mutation UpdateInstrumentationRule($ruleId: ID!, $instrumentationRule: InstrumentationRuleInput!) {
    updateInstrumentationRule(ruleId: $ruleId, instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      managedBy
      sourcesScopes { ${SOURCES_SCOPE_FIELDS} }
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
      customInstrumentations {
        golang {
          packageName
          functionName
          receiverName
          receiverMethodName
        }
        java {
          methodName
          className
        }
        php {
          className
          functionName
        }
      }
    }
  }`,
    'v1.33': `
  mutation UpdateInstrumentationRule($ruleId: ID!, $instrumentationRule: InstrumentationRuleInput!) {
    updateInstrumentationRule(ruleId: $ruleId, instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      sourcesScopes { ${SOURCES_SCOPE_FIELDS} }
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
      customInstrumentations {
        golang {
          packageName
          functionName
          receiverName
          receiverMethodName
        }
        java {
          methodName
          className
        }
        php {
          className
          functionName
        }
      }
    }
  }`,
    // Starts at v1.26, where `sourcesScopes` replaced `workloads` (was keyed 'v1.28').
    'v1.26': `
  mutation UpdateInstrumentationRule($ruleId: ID!, $instrumentationRule: InstrumentationRuleInput!) {
    updateInstrumentationRule(ruleId: $ruleId, instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      sourcesScopes { ${SOURCES_SCOPE_FIELDS} }
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
      customInstrumentations {
        golang {
          packageName
          functionName
          receiverName
          receiverMethodName
        }
        java {
          methodName
          className
        }
      }
    }
  }`,
    'v1.11': `
  mutation UpdateInstrumentationRule($ruleId: ID!, $instrumentationRule: InstrumentationRuleInput!) {
    updateInstrumentationRule(ruleId: $ruleId, instrumentationRule: $instrumentationRule) {
      type
      ruleId
      ruleName
      notes
      disabled
      mutable
      profileName
      # Echoed back so a later edit doesn't clear scope (update is a full replace).
      workloads {
        namespace
        kind
        name
      }
      payloadCollection {
        httpRequest {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        httpResponse {
          mimeTypes
          maxPayloadLength
          dropPartialPayloads
        }
        dbQuery {
          maxPayloadLength
          dropPartialPayloads
        }
        messaging {
          maxPayloadLength
          dropPartialPayloads
        }
      }
      codeAttributes {
        column
        filePath
        function
        lineNumber
        namespace
        stacktrace
      }
      headersCollection {
        headerKeys
      }
      customInstrumentations {
        golang {
          packageName
          functionName
          receiverName
          receiverMethodName
        }
        java {
          methodName
          className
        }
      }
    }
  }`,
  },
};

export const DELETE_INSTURMENTATION_RULE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation DeleteInstrumentationRule($ruleId: ID!) {
    deleteInstrumentationRule(ruleId: $ruleId)
  }`,
  },
};
