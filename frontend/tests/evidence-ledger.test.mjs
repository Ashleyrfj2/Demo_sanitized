import assert from 'node:assert/strict';
import test from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { ClaimCard, EvidenceLedgerView } from '../dist-test/index.mjs';

const mockBaseSummary = {
  claim_id: 'claim-001',
  tenant_id: 'tenant-fest-01',
  target: 'canopy_supply_item',
  state_signature: 'revoked_preserved',
  actor_context: 'editor',
  validation_intent: 'verify_revocation',
  classification: 'Solid',
  heuristic_score: 0.9,
  max_verification_depth: 3,
  freshness: 'current',
  independent_source_count: 2,
  compatible_observation_count: 3,
  conflict_count: 0,
  latest_observation_at: '2026-08-23T20:00:00Z',
  reasons: ['Corroborated by depth-3 backend/persistence verification satisfying validation intent (2 independent source(s), zero conflicts).'],
  evidence_contexts: [
    {
      id: 'ctx-001',
      tenant_id: 'tenant-fest-01',
      run_id: 'run-guided-001',
      build_id: 'festnest-001',
      environment_id: 'festnest-local-browser',
      scenario_id: 'equipment-handoff-v1',
      feature_flags: { camp_grid_v2: 'true' },
      device: 'desktop',
      region: 'us-east-1',
      created_at: '2026-08-23T19:50:00Z',
    },
  ],
};

test('ClaimCard renders all six derived classifications verbatim without recomputing', () => {
  const classifications = ['Solid', 'Weak', 'Stale', 'Conflicted', 'Untouched', 'Blocked'];

  for (const classification of classifications) {
    const summary = {
      ...mockBaseSummary,
      claim_id: `claim-${classification.toLowerCase()}`,
      classification,
      heuristic_score: classification === 'Conflicted' ? null : 0.5,
      conflict_count: classification === 'Conflicted' ? 1 : 0,
    };

    const html = renderToStaticMarkup(React.createElement(ClaimCard, { summary }));

    // 1. Must contain exact classification text
    assert.ok(html.includes(classification), `HTML must include classification ${classification}`);

    // 2. Must contain status class pill
    assert.ok(html.includes(`status-${classification}`), `HTML must include status-${classification}`);

    // 3. Must use classification as returned from backend (no recomputation)
    assert.ok(html.includes(`claim-card classification-${classification.toLowerCase()}`));
  }
});

test('ClaimCard renders reconciliation reason text and semantic signature', () => {
  const summary = {
    ...mockBaseSummary,
    reasons: [
      'Database mutation denied as expected for non-owner.',
      'Corroborated across 2 independent verifiers.',
    ],
  };

  const html = renderToStaticMarkup(React.createElement(ClaimCard, { summary }));

  assert.ok(html.includes('Database mutation denied as expected for non-owner.'));
  assert.ok(html.includes('Corroborated across 2 independent verifiers.'));
  assert.ok(html.includes('Reconciliation Rationale'));
  assert.ok(html.includes('canopy_supply_item'));
  assert.ok(html.includes('revoked_preserved'));
  assert.ok(html.includes('editor'));
  assert.ok(html.includes('verify_revocation'));
});

test('ClaimCard renders verification depth, metrics, and active conflict highlight', () => {
  // Depth 3 with conflict
  const summaryConflicted = {
    ...mockBaseSummary,
    classification: 'Conflicted',
    heuristic_score: null,
    max_verification_depth: 3,
    conflict_count: 2,
    reasons: ['Credible evidence disagrees on this claim: 1 supporting vs 2 conflicting.'],
  };

  const html = renderToStaticMarkup(React.createElement(ClaimCard, { summary: summaryConflicted }));

  assert.ok(html.includes('Depth 3 · Backend Persistence'));
  assert.ok(html.includes('2 active'));
  assert.ok(html.includes('conflict-highlight'));
  assert.ok(html.includes('2 independent'));
  assert.ok(html.includes('3 compatible'));

  // Depth 1 surface DOM
  const summarySurface = {
    ...mockBaseSummary,
    classification: 'Weak',
    max_verification_depth: 1,
    conflict_count: 0,
  };
  const htmlSurface = renderToStaticMarkup(React.createElement(ClaimCard, { summary: summarySurface }));
  assert.ok(htmlSurface.includes('Depth 1 · Surface DOM'));
});

test('EvidenceLedgerView renders empty and untouched state correctly', () => {
  const htmlEmpty = renderToStaticMarkup(
    React.createElement(EvidenceLedgerView, {
      summaries: [],
      loading: false,
      error: null,
      onRefresh: () => {},
    })
  );

  assert.ok(htmlEmpty.includes('Build Validation &amp; Evidence Ledger') || htmlEmpty.includes('Build Validation & Evidence Ledger'));
  assert.ok(htmlEmpty.includes('Total Claims'));
  assert.ok(htmlEmpty.includes('No claim definitions or evidence found'));
  assert.ok(htmlEmpty.includes('A state visit is not knowledge'));
});

test('EvidenceLedgerView renders loading and error states', () => {
  // Loading state
  const htmlLoading = renderToStaticMarkup(
    React.createElement(EvidenceLedgerView, {
      summaries: [],
      loading: true,
      error: null,
      onRefresh: () => {},
    })
  );
  assert.ok(htmlLoading.includes('Reconciling claim evidence for active scope'));

  // Error state
  const htmlError = renderToStaticMarkup(
    React.createElement(EvidenceLedgerView, {
      summaries: [mockBaseSummary],
      loading: false,
      error: 'Backend API connection timeout on loopback',
      onRefresh: () => {},
    })
  );
  assert.ok(htmlError.includes('Backend API connection timeout on loopback'));
  assert.ok(htmlError.includes('claim-card-claim-001'));
});

test('EvidenceLedgerView displays classification counter chips for all six categories', () => {
  const summaries = [
    { ...mockBaseSummary, claim_id: 'c1', classification: 'Solid' },
    { ...mockBaseSummary, claim_id: 'c2', classification: 'Weak' },
    { ...mockBaseSummary, claim_id: 'c3', classification: 'Conflicted' },
    { ...mockBaseSummary, claim_id: 'c4', classification: 'Blocked' },
    { ...mockBaseSummary, claim_id: 'c5', classification: 'Stale' },
    { ...mockBaseSummary, claim_id: 'c6', classification: 'Untouched' },
  ];

  const html = renderToStaticMarkup(
    React.createElement(EvidenceLedgerView, {
      summaries,
      loading: false,
      error: null,
      onRefresh: () => {},
    })
  );

  assert.ok(html.includes('Total Claims'));
  assert.ok(html.includes('Solid'));
  assert.ok(html.includes('Weak'));
  assert.ok(html.includes('Conflicted'));
  assert.ok(html.includes('Blocked'));
  assert.ok(html.includes('Stale'));
  assert.ok(html.includes('Untouched'));
});
