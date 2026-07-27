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
    'v1.28': `
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
    'v1.28': `
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
