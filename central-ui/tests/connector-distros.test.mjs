import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { createRequire } from 'node:module';
import ts from 'typescript';
const require=createRequire(import.meta.url);
const load=(file, dependencies={})=>{
 const js=ts.transpileModule(readFileSync(new URL(file,import.meta.url),'utf8'),{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText;
 const mod={exports:{}};
 new Function('require','module','exports',js)(id=>dependencies[id]??require(id),mod,mod.exports);
 return mod.exports;
};
const utils=load('../src/graphql/remote/utils.ts');
const dialect={dialectVersion:ctx=>ctx.schemaVersion??ctx.version};
const reports=load('../src/graphql/remote/vm-workload-reports.ts',{'@/graphql/remote/utils':utils,'@/graphql/remote/vm-dialect':dialect,'@odigos/ui-kit/types':{DesiredStateProgress:{Success:'Success'},ProgrammingLanguages:{Java:'Java'}}});
const sources=load('../src/graphql/remote/vm-sources.ts',{'@/graphql/remote/utils':utils,'@/graphql/remote/vm-dialect':dialect,'@/graphql/remote/vm-workload-reports':reports});
test('SDK catalog selections are version gated and never request handler metadata',()=>{
 const query='query Test { sources { config { distroOverride { otelDistroName } } } workloadReports { desired { sdkType } } }';
 assert.doesNotMatch(sources.patchVmSourcesWithReportsQuery(query,{version:'0.27.0'}),/availableDistros/);
 const patched=sources.patchVmSourcesWithReportsQuery(query,{version:'0.28.0'});
 assert.match(patched,/availableDistros/);assert.doesNotMatch(patched,/handlerWrapper/);
 assert.doesNotMatch(sources.patchVmSourceQuery(query,{version:'1.0.0'}),/handlerWrapper/);
 assert.equal(utils.supportsInstrumentationDistributions('0.27.0'),false);
});
test('every handler SDK selection survives source reads, updates and clearing with one field',()=>{
 for(const name of ['java-lambda-otel-agent','java-lambda-otel-wrapper','java-lambda-otel-proxy-wrapper','java-lambda-otel-sqs-wrapper','java-lambda-otel-stream-wrapper']){
  const source={name:'fn',kind:'aws.lambda',region:'us-east-2',enabled:true,config:{distroOverride:{otelDistroName:name}}};
  const workload=sources.mapVmSourceToWorkload(source);
  assert.deepEqual(workload.sourceConfig.distroOverride,{otelDistroName:name});
  const variables=sources.vmUpdateSourceVariables({sourceId:workload.id,patchSourceRequest:{otelDistroName:name}},{version:'0.28.0'});
  assert.deepEqual(variables.input.config.distroOverride,{otelDistroName:name});
  const cleared=sources.vmUpdateSourceVariables({sourceId:workload.id,patchSourceRequest:{otelDistroName:null}},{version:'0.28.0'});
  assert.equal(cleared.input.config.distroOverride,null);
 }
});
