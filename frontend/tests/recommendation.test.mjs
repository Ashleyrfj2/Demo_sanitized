import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { RecommendationView } from '../dist-test/index.mjs';

const mockRecommendation = {
  id: 'rec-fest-001',
  tenant_id: 'festival-thesis-demo',
  claim_id: 'FEST-CLAIM-04',
  run_id: 'festival-guided-001',
  actor_id: 'late-tester-d',
  role: 'editor',
  build_id: 'festnest-demo-001',
  environment_id: 'festnest-local-browser',
  scenario_id: 'equipment-handoff-v1',
  area: 'Supply List / canopy persistence',
  classification: 'Weak',
  max_verification_depth: 1,
  independent_source_count: 1,
  conflict_count: 0,
  rationale: 'Supply List / canopy persistence (FEST-CLAIM-04) is Weak at depth 1 with 1 independent source(s) and 0 conflict(s). Factors: uncertainty reduction 0.80 x business risk 0.90 x diff impact 0.50 x smoothed failure prior 0.25 / expected cost 1.00 = priority 0.0900 under policy information-value-v0.1.',
  score: 0.09,
  factors: {
    delta_uncertainty: 0.8,
    business_risk: 0.9,
    diff_impact: 0.5,
    smoothed_failure_prior: 0.25,
    expected_cost: 1.0,
  },
  policy_version: 'information-value-v0.1',
  evidence_snapshot: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
  progression_snapshot: 'b3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b844',
  override_alternatives: [
    {
      claim_id: 'FEST-CLAIM-08', title: 'Supply List ownership authorization',
      area: 'Supply List / ownership authorization', classification: 'Conflicted',
    },
  ],
  created_at: '2026-08-25T20:00:00Z',
};

function renderView(overrides = {}) {
  return renderToStaticMarkup(
    React.createElement(RecommendationView, {
      recommendation: mockRecommendation,
      reason: '',
      onReasonChange: () => {},
      overrideClaimId: '',
      onOverrideClaimChange: () => {},
      onRespond: () => {},
      busy: false,
      ...overrides,
    })
  );
}

test('RecommendationView renders empty/waiting state when recommendation is null', () => {
  const html = renderView({ recommendation: null });
  assert.ok(html.includes('Next high-value area · Late Tester D'));
  assert.ok(html.includes('Waiting for qualifying claim evidence'));
  assert.ok(html.includes('A recommendation will appear after a claim has an eligible validation path.'));
  assert.ok(!html.includes('response-panel'), 'empty state must not render response panel');
  assert.ok(!html.includes('class="factors"'), 'empty state must not render factors');
});

test('RecommendationView renders active recommendation factors, priority, policy, and claim ID', () => {
  const html = renderView();
  assert.ok(html.includes('Supply List / canopy persistence'));
  assert.ok(html.includes('is Weak at depth 1'));
  assert.ok(html.includes('under policy information-value-v0.1'));
  assert.ok(html.includes('<span>claim FEST-CLAIM-04</span>'));
  assert.ok(html.includes('<span>priority 0.0900</span>'));
  assert.ok(html.includes('<span>policy information-value-v0.1</span>'));
  assert.ok(html.includes('<span>delta uncertainty 0.80</span>'));
  assert.ok(html.includes('<span>business risk 0.90</span>'));
  assert.ok(html.includes('<span>diff impact 0.50</span>'));
  assert.ok(html.includes('<span>smoothed failure prior 0.25</span>'));
  assert.ok(html.includes('<span>expected cost 1.00</span>'));
});

test('RecommendationView excludes zero-value factors from factor chips', () => {
  const html = renderView({
    recommendation: {
      ...mockRecommendation,
      factors: { ...mockRecommendation.factors, delta_uncertainty: 0, diff_impact: 0.75 },
    },
  });
  assert.ok(!html.includes('delta uncertainty'), 'zero-value factor must be omitted');
  assert.ok(html.includes('<span>diff impact 0.75</span>'));
});

test('RecommendationView renders only backend-supplied override alternatives', () => {
  const html = renderView();
  assert.ok(html.includes('FEST-CLAIM-08 · Supply List ownership authorization · Conflicted'));
  assert.ok(!html.includes('FEST-CLAIM-05'), 'known role-infeasible claim is absent because the backend did not supply it');
  assert.ok(!html.includes('value="FEST-CLAIM-04"'), 'current recommendation cannot be its own override target');
});

test('RecommendationView requires a reason for accept and dismiss', () => {
  const empty = renderView();
  assert.match(empty, /<button[^>]*disabled[^>]*>Accept area<\/button>/);
  assert.match(empty, /<button[^>]*disabled[^>]*>Dismiss<\/button>/);

  const whitespace = renderView({ reason: '   \t \n  ' });
  assert.match(whitespace, /<button[^>]*disabled[^>]*>Accept area<\/button>/);
  assert.match(whitespace, /<button[^>]*disabled[^>]*>Dismiss<\/button>/);
});

test('RecommendationView enables accept and dismiss for a valid reason', () => {
  const html = renderView({ reason: 'Area aligns with unresolved canopy persistence.' });
  assert.doesNotMatch(html, /<button[^>]*disabled[^>]*>Accept area<\/button>/);
  assert.doesNotMatch(html, /<button[^>]*disabled[^>]*>Dismiss<\/button>/);
  assert.ok(html.includes('Area aligns with unresolved canopy persistence.'));
});

test('RecommendationView enables override only when reason and a valid alternative are selected', () => {
  const reasonOnly = renderView({ reason: 'Authorization is the more important uncertainty.' });
  assert.match(reasonOnly, /<button[^>]*disabled[^>]*>Override<\/button>/);

  const selected = renderView({
    reason: 'Authorization is the more important uncertainty.',
    overrideClaimId: 'FEST-CLAIM-08',
  });
  assert.doesNotMatch(selected, /<button[^>]*disabled[^>]*>Override<\/button>/);

  const unavailable = renderView({
    reason: 'Choose another area.',
    overrideClaimId: 'FEST-CLAIM-99',
  });
  assert.match(unavailable, /<button[^>]*disabled[^>]*>Override<\/button>/);
});

test('RecommendationView disables all response actions when busy', () => {
  const html = renderView({
    reason: 'Area aligns with unresolved canopy persistence.',
    overrideClaimId: 'FEST-CLAIM-08',
    busy: true,
  });
  assert.match(html, /<button[^>]*disabled[^>]*>Accept area<\/button>/);
  assert.match(html, /<button[^>]*disabled[^>]*>Override<\/button>/);
  assert.match(html, /<button[^>]*disabled[^>]*>Dismiss<\/button>/);
});
