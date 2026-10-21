#!/usr/bin/env node

import { readFile, readdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(fileURLToPath(new URL('..', import.meta.url)));
const schema = JSON.parse(await readFile(resolve(root, 'contracts/validation-event.schema.json'), 'utf8'));
const openapi = await readFile(resolve(root, 'contracts/openapi.yaml'), 'utf8');
const goModel = await readFile(resolve(root, 'backend/internal/model/validation_event.go'), 'utf8');
const typeScript = await readFile(resolve(root, 'frontend/src/types/validation-event.ts'), 'utf8');

function fail(message) { throw new Error(`contract conformance: ${message}`); }
function sorted(values) { return [...values].sort(); }
function assertEqual(label, left, right) {
  if (JSON.stringify(sorted(left)) !== JSON.stringify(sorted(right))) fail(`${label} differs: ${JSON.stringify(sorted(left))} vs ${JSON.stringify(sorted(right))}`);
}

const schemaFields = Object.keys(schema.properties);
const goBlock = goModel.match(/type ValidationEvent struct \{([\s\S]*?)\n\}/)?.[1];
if (!goBlock) fail('Go ValidationEvent struct was not found');
const goFields = [...goBlock.matchAll(/`json:"([^",]+)(?:,[^"]*)?"`/g)].map((match) => match[1]);
const tsBlock = typeScript.match(/export interface ValidationEvent \{([\s\S]*?)\n\}/)?.[1];
if (!tsBlock) fail('TypeScript ValidationEvent interface was not found');
const tsFields = [...tsBlock.matchAll(/^\s*([a-z_]+)\??:/gm)].map((match) => match[1]);
assertEqual('schema and Go fields', schemaFields, goFields);
assertEqual('schema and TypeScript fields', schemaFields, tsFields);

const optionalSchema = schemaFields.filter((field) => !schema.required.includes(field));
const optionalGo = [...goBlock.matchAll(/`json:"([^",]+),omitempty"`/g)].map((match) => match[1]);
const optionalTS = [...tsBlock.matchAll(/^\s*([a-z_]+)\?:/gm)].map((match) => match[1]);
assertEqual('schema and Go optional fields', optionalSchema, optionalGo);
assertEqual('schema and TypeScript optional fields', optionalSchema, optionalTS);

const recommendationGoBlock = goModel.match(/type Recommendation struct \{([\s\S]*?)\n\}/)?.[1];
if (!recommendationGoBlock) fail('Go Recommendation struct was not found');
const recommendationGoFields = [...recommendationGoBlock.matchAll(/`json:"([^",]+)(?:,[^"]*)?"`/g)].map((match) => match[1]);
const recommendationTSBlock = typeScript.match(/export interface Recommendation \{([\s\S]*?)\n\}/)?.[1];
if (!recommendationTSBlock) fail('TypeScript Recommendation interface was not found');
const recommendationTSFields = [...recommendationTSBlock.matchAll(/^\s*([a-z_]+)\??:/gm)].map((match) => match[1]);
assertEqual('Go and TypeScript Recommendation fields', recommendationGoFields, recommendationTSFields);
const recommendationOptionalGo = [...recommendationGoBlock.matchAll(/`json:"([^",]+),omitempty"`/g)].map((match) => match[1]);
const recommendationOptionalTS = [...recommendationTSBlock.matchAll(/^\s*([a-z_]+)\?:/gm)].map((match) => match[1]);
assertEqual('Go and TypeScript Recommendation optional fields', recommendationOptionalGo, recommendationOptionalTS);

const recommendationFactorsGoBlock = goModel.match(/type RecommendationFactors struct \{([\s\S]*?)\n\}/)?.[1];
const recommendationFactorsTSBlock = typeScript.match(/export interface RecommendationFactors \{([\s\S]*?)\n\}/)?.[1];
if (!recommendationFactorsGoBlock || !recommendationFactorsTSBlock) fail('RecommendationFactors model was not found');
const recommendationFactorsGoFields = [...recommendationFactorsGoBlock.matchAll(/`json:"([^",]+)"`/g)].map((match) => match[1]);
const recommendationFactorsTSFields = [...recommendationFactorsTSBlock.matchAll(/^\s*([a-z_]+):/gm)].map((match) => match[1]);
assertEqual('Go and TypeScript RecommendationFactors fields', recommendationFactorsGoFields, recommendationFactorsTSFields);

const recommendationFactorsOpenAPI = openapi.match(/^    RecommendationFactors:\n([\s\S]*?)^    Recommendation:/m)?.[1];
if (!recommendationFactorsOpenAPI) fail('OpenAPI RecommendationFactors schema was not found');
const recommendationOpenAPI = openapi.match(/^    Recommendation:\n([\s\S]*?)^    RecommendationResponse:/m)?.[1];
if (!recommendationOpenAPI) fail('OpenAPI Recommendation schema was not found');
const recommendationRequired = recommendationOpenAPI.match(/^      required: \[([^\]]+)\]/m)?.[1].split(',').map((field) => field.trim()) ?? [];
const expectedRecommendationRequired = recommendationGoFields.filter((field) => !recommendationOptionalGo.includes(field));
assertEqual('OpenAPI and Go required Recommendation fields', recommendationRequired, expectedRecommendationRequired);
for (const field of recommendationGoFields) {
  if (!recommendationOpenAPI.includes(`        ${field}:`)) fail(`OpenAPI Recommendation is missing ${field}`);
}
for (const field of recommendationFactorsGoFields) {
  if (!recommendationFactorsOpenAPI.includes(`${field}:`)) fail(`OpenAPI RecommendationFactors is missing ${field}`);
}

const recommendationResponseGoBlock = goModel.match(/type RecommendationResponse struct \{([\s\S]*?)\n\}/)?.[1];
const recommendationResponseTSBlock = typeScript.match(/export interface RecommendationResponse \{([\s\S]*?)\n\}/)?.[1];
if (!recommendationResponseGoBlock || !recommendationResponseTSBlock) fail('RecommendationResponse model was not found');
const recommendationResponseGoFields = [...recommendationResponseGoBlock.matchAll(/`json:"([^",]+)(?:,[^"]*)?"`/g)].map((match) => match[1]);
const recommendationResponseTSFields = [...recommendationResponseTSBlock.matchAll(/^\s*([a-z_]+)\??:/gm)].map((match) => match[1]);
assertEqual('Go and TypeScript RecommendationResponse fields', recommendationResponseGoFields, recommendationResponseTSFields);
const recommendationResponseOptionalGo = [...recommendationResponseGoBlock.matchAll(/`json:"([^",]+),omitempty"`/g)].map((match) => match[1]);
const recommendationResponseOptionalTS = [...recommendationResponseTSBlock.matchAll(/^\s*([a-z_]+)\?:/gm)].map((match) => match[1]);
assertEqual('Go and TypeScript RecommendationResponse optional fields', recommendationResponseOptionalGo, recommendationResponseOptionalTS);

const recommendationResponseOpenAPI = openapi.match(/^    RecommendationResponse:\n([\s\S]*?)^security:/m)?.[1];
if (!recommendationResponseOpenAPI) fail('OpenAPI RecommendationResponse schema was not found');
const recommendationResponseRequired = recommendationResponseOpenAPI.match(/^      required: \[([^\]]+)\]/m)?.[1].split(',').map((field) => field.trim()) ?? [];
const expectedRecommendationResponseRequired = recommendationResponseGoFields.filter((field) => !recommendationResponseOptionalGo.includes(field));
assertEqual('OpenAPI and Go required RecommendationResponse fields', recommendationResponseRequired, expectedRecommendationResponseRequired);
for (const field of recommendationResponseGoFields) {
  if (!recommendationResponseOpenAPI.includes(`        ${field}:`)) fail(`OpenAPI RecommendationResponse is missing ${field}`);
}

const claimGoModel = await readFile(resolve(root, 'backend/internal/model/claim.go'), 'utf8');
const claimTSModel = await readFile(resolve(root, 'frontend/src/types/claim-evidence.ts'), 'utf8');

const claimDefGoBlock = claimGoModel.match(/type ClaimDefinition struct \{([\s\S]*?)\n\}/)?.[1];
if (!claimDefGoBlock) fail('Go ClaimDefinition struct was not found');
const claimDefGoFields = [...claimDefGoBlock.matchAll(/`json:"([^",]+)(?:,[^"]*)?"`/g)].map((match) => match[1]);
const claimDefOpenAPI = openapi.match(/^    ClaimDefinition:\n([\s\S]*?)^    EvidenceContext:/m)?.[1];
if (!claimDefOpenAPI) fail('OpenAPI ClaimDefinition schema was not found');
for (const field of claimDefGoFields) {
  if (!claimDefOpenAPI.includes(`        ${field}:`)) fail(`OpenAPI ClaimDefinition is missing ${field}`);
}

const ecGoBlock = claimGoModel.match(/type EvidenceContext struct \{([\s\S]*?)\n\}/)?.[1];
if (!ecGoBlock) fail('Go EvidenceContext struct was not found');
const ecGoFields = [...ecGoBlock.matchAll(/`json:"([^",]+)(?:,[^"]*)?"`/g)].map((match) => match[1]);
const ecTSBlock = claimTSModel.match(/export interface EvidenceContext \{([\s\S]*?)\n\}/)?.[1];
if (!ecTSBlock) fail('TypeScript EvidenceContext interface was not found');
const ecTSFields = [...ecTSBlock.matchAll(/^\s*([a-z_]+)\??:/gm)].map((match) => match[1]);
assertEqual('Go and TypeScript EvidenceContext fields', ecGoFields, ecTSFields);
const ecOpenAPI = openapi.match(/^    EvidenceContext:\n([\s\S]*?)^    Observation:/m)?.[1];
if (!ecOpenAPI) fail('OpenAPI EvidenceContext schema was not found');
for (const field of ecGoFields) {
  if (!ecOpenAPI.includes(`        ${field}:`)) fail(`OpenAPI EvidenceContext is missing ${field}`);
}

const obsGoBlock = claimGoModel.match(/type Observation struct \{([\s\S]*?)\n\}/)?.[1];
if (!obsGoBlock) fail('Go Observation struct was not found');
const obsGoFields = [...obsGoBlock.matchAll(/`json:"([^",]+)(?:,[^"]*)?"`/g)].map((match) => match[1]);
const obsOpenAPI = openapi.match(/^    Observation:\n([\s\S]*?)^    ClaimEvidenceSummariesResponse:/m)?.[1];
if (!obsOpenAPI) fail('OpenAPI Observation schema was not found');
for (const field of obsGoFields) {
  if (!obsOpenAPI.includes(`        ${field}:`)) fail(`OpenAPI Observation is missing ${field}`);
}
const obsAssessments = obsOpenAPI.match(/assessment:\s*\{\s*enum:\s*\[([^\]]+)\]\s*\}/)?.[1].split(',').map((s) => s.trim()) ?? [];
assertEqual('Observation assessment enum values', ['supports', 'weakens', 'conflicts', 'invalidates', 'blocked'], obsAssessments);

const summaryGoBlock = claimGoModel.match(/type ClaimEvidenceSummary struct \{([\s\S]*?)\n\}/)?.[1];
if (!summaryGoBlock) fail('Go ClaimEvidenceSummary struct was not found');
const summaryGoFields = [...summaryGoBlock.matchAll(/`json:"([^",]+)(?:,[^"]*)?"`/g)].map((match) => match[1]);
const summaryTSBlock = claimTSModel.match(/export interface ClaimEvidenceSummary \{([\s\S]*?)\n\}/)?.[1];
if (!summaryTSBlock) fail('TypeScript ClaimEvidenceSummary interface was not found');
const summaryTSFields = [...summaryTSBlock.matchAll(/^\s*([a-z_]+)\??:/gm)].map((match) => match[1]);
assertEqual('Go and TypeScript ClaimEvidenceSummary fields', summaryGoFields, summaryTSFields);
const summaryOpenAPI = openapi.match(/^    ClaimEvidenceSummary:\n([\s\S]*?)^    ClaimDefinition:/m)?.[1];
if (!summaryOpenAPI) fail('OpenAPI ClaimEvidenceSummary schema was not found');
for (const field of summaryGoFields) {
  if (!summaryOpenAPI.includes(`        ${field}:`)) fail(`OpenAPI ClaimEvidenceSummary is missing ${field}`);
}

if (!openapi.includes('$ref: ./validation-event.schema.json')) fail('OpenAPI ingest does not reference the canonical schema');
for (const status of ['"202"', '"200"', '"401"', '"403"', '"409"', '"422"']) {
  if (!openapi.includes(status)) fail(`OpenAPI is missing ingest response ${status}`);
}

const fixtureDirectory = resolve(root, 'testdata/trajectories');
for (const name of (await readdir(fixtureDirectory)).filter((entry) => entry.endsWith('.json'))) {
  const fixture = JSON.parse(await readFile(resolve(fixtureDirectory, name), 'utf8'));
  const unknown = Object.keys(fixture).filter((field) => !schemaFields.includes(field));
  if (unknown.length) fail(`${name} has unknown fields: ${unknown.join(', ')}`);
  for (const required of schema.required) if (!(required in fixture)) fail(`${name} is missing ${required}`);
  if (!fixture.session_id && !fixture.run_id) fail(`${name} needs session_id or run_id`);
  if (!schema.properties.source_type.enum.includes(fixture.source_type)) fail(`${name} has invalid source_type`);
  if (!schema.properties.outcome.enum.includes(fixture.outcome)) fail(`${name} has invalid outcome`);
  if (!fixture.privacy?.masked) fail(`${name} must declare masked evidence`);
  if (!Number.isInteger(fixture.step_index) || fixture.step_index < 0) fail(`${name} has invalid step_index`);
  if (Number.isNaN(Date.parse(fixture.occurred_at))) fail(`${name} has invalid occurred_at`);
}

console.log('ValidationEvent, Recommendation, Claim, EvidenceContext, Observation schemas, OpenAPI, Go, TypeScript, and trajectory fixtures conform.');
