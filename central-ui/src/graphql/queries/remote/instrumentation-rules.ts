import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// Selection set for the InstrumentationRule's row-per-criterion `sourcesScopes`.
// Each row carries one of (workloadName + workloadKind), workloadNamespace, or
// workloadLanguage. The kit consumes the spec-shape `SourcesScopes` instead, so
// callers must adapt the response via `adaptInstrumentationRuleFromWire`.
const SOURCES_SCOPE_FIELDS = `
  workloadName
  workloadKind
  workloadNamespace
  workloadLanguage
`;

// Dynamic instrumentation-rule-type catalog (YAML-driven), mirroring
// GET_ACTION_TYPES. K8s: introduced in odigos v1.31 (the `instrumentationRuleTypes`
// resolver is unreleased as of v1.30.x). Proxies older than that have no bucket here ->
// `getVersionedQuery` returns undefined -> `canRun` is false and the kit falls
// back to its hard-coded INSTRUMENTATION_RULE_OPTIONS.
export const GET_INSTRUMENTATION_RULE_TYPES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.31': `
  query GetInstrumentationRuleTypes {
    instrumentationRuleTypes {
      type
      displayName
      description
      supportedLanguages
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
  // VM proxies resolve the rule catalog from the versioned vmagent
  // `instrumentationRuleTypes` dialect document (see the VmSpec on
  // GET_INSTRUMENTATION_RULE_TYPES in odigos-api-adapter).
};

export const GET_INSTRUMENTATION_RULES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.28': `
  query GetInstrumentationRules {
    computePlatform {
      instrumentationRules {
        type
        ruleId
        ruleName
        notes
        disabled
        mutable
        profileName
        sourcesScopes { ${SOURCES_SCOPE_FIELDS} }
        conditions {
          status
          type
          reason
          message

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
    }
  }`,
    'v1.11': `
  query GetInstrumentationRules {
    computePlatform {
      instrumentationRules {
        type
        ruleId
        ruleName
        notes
        disabled
        mutable
        profileName
        conditions {
          status
          type
          reason
          message

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
    }
  }`,
  },
};
