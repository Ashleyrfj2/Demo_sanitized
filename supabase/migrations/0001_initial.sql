create extension if not exists pgcrypto;

create type source_type as enum ('human', 'agent', 'automation');
create type validation_outcome as enum ('pass', 'fail', 'blocked', 'ambiguous', 'abandoned');
create type evidence_status as enum (
  'strongly_represented', 'weak_or_uncertain', 'conflicting', 'stale',
  'blocked', 'untouched', 'needs_independent_verification'
);

create table tenants (
  id text primary key,
  name text not null,
  created_at timestamptz not null default now()
);

create table actors (
  tenant_id text not null references tenants(id),
  id text not null,
  display_name text,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id)
);

create table experiment_runs (
  id text primary key,
  tenant_id text not null references tenants(id),
  build_id text not null,
  environment_id text not null,
  scenario_id text,
  mode text not null check (mode in ('baseline', 'guided', 'unspecified')),
  started_at timestamptz not null default now(),
  frozen_at timestamptz
);

create table validation_events (
  event_id text primary key,
  tenant_id text not null references tenants(id),
  actor_id text not null,
  session_id text,
  run_id text,
  source source_type not null,
  source_adapter text not null,
  build_id text not null,
  environment_id text not null,
  context jsonb not null default '{}'::jsonb,
  route text not null,
  state_fingerprint text not null,
  action_type text not null,
  outcome validation_outcome not null,
  verifier_result text,
  finding_id text,
  verification_status text,
  occurred_at timestamptz not null,
  step_index integer not null check (step_index >= 0),
  privacy jsonb not null default '{"masked": true}'::jsonb,
  raw_event jsonb not null,
  created_at timestamptz not null default now(),
  foreign key (tenant_id, actor_id) references actors(tenant_id, id),
  check (session_id is not null or run_id is not null)
);

create table candidate_states (
  id text primary key,
  tenant_id text not null references tenants(id),
  build_id text not null,
  environment_id text not null,
  role text not null default 'unknown',
  module text not null default 'unknown',
  record_id text not null default 'unknown',
  business_state text not null default 'unknown',
  state_fingerprint text not null,
  status evidence_status not null default 'weak_or_uncertain',
  evidence_count integer not null default 0,
  independent_actor_count integer not null default 0,
  source_types jsonb not null default '[]'::jsonb,
  conflict boolean not null default false,
  updated_at timestamptz not null default now(),
  unique (tenant_id, build_id, environment_id, role, state_fingerprint)
);

create table evidence_records (
  id text primary key,
  tenant_id text not null references tenants(id),
  candidate_state_id text not null references candidate_states(id),
  event_id text not null unique references validation_events(event_id),
  actor_id text not null,
  source source_type not null,
  outcome validation_outcome not null,
  occurred_at timestamptz not null,
  created_at timestamptz not null default now()
);

create table state_corrections (
  id text primary key,
  tenant_id text not null references tenants(id),
  build_id text not null,
  environment_id text not null,
  correction_type text not null check (correction_type in ('merge', 'split', 'rename')),
  state_id text not null,
  before_state jsonb not null,
  after_state jsonb not null,
  corrected_by text not null,
  reason text not null,
  created_at timestamptz not null default now()
);

create table verification_requests (
  id text primary key,
  tenant_id text not null references tenants(id),
  candidate_state_id text not null references candidate_states(id),
  build_id text not null,
  environment_id text not null,
  requested_role text,
  rationale text not null,
  status text not null default 'open' check (status in ('open', 'completed', 'dismissed')),
  created_at timestamptz not null default now(),
  resolved_at timestamptz
);

create table recommendation_events (
  id text primary key,
  tenant_id text not null references tenants(id),
  candidate_state_id text references candidate_states(id),
  actor_id text not null,
  role text not null,
  build_id text not null,
  environment_id text not null,
  run_id text references experiment_runs(id),
  area text not null,
  rationale text not null,
  score numeric not null,
  factors jsonb not null,
  created_at timestamptz not null default now()
);

create table recommendation_responses (
  id text primary key,
  recommendation_id text not null references recommendation_events(id),
  response text not null check (response in ('accepted', 'dismissed')),
  reason text not null,
  actor_id text not null,
  created_at timestamptz not null default now()
);

create table experiment_metrics (
  id text primary key,
  run_id text not null references experiment_runs(id),
  definition_version text not null,
  metrics jsonb not null,
  computed_at timestamptz not null default now(),
  unique (run_id, definition_version)
);

create index validation_events_scope_idx on validation_events (tenant_id, build_id, environment_id);
create index validation_events_actor_run_idx on validation_events (actor_id, session_id, run_id);
create index validation_events_state_idx on validation_events (route, state_fingerprint);
create index validation_events_source_outcome_time_idx on validation_events (source, outcome, occurred_at);
create index candidate_states_scope_status_idx on candidate_states (tenant_id, build_id, environment_id, status);
create index evidence_records_candidate_time_idx on evidence_records (candidate_state_id, occurred_at);
create index verification_requests_scope_status_idx on verification_requests (tenant_id, build_id, environment_id, status);
create index recommendation_events_actor_build_idx on recommendation_events (actor_id, build_id, created_at desc);
create index recommendation_responses_recommendation_idx on recommendation_responses (recommendation_id);
create index experiment_metrics_run_idx on experiment_metrics (run_id, definition_version);

create function reject_validation_event_mutation()
returns trigger language plpgsql as $$
begin
  raise exception 'validation_events are immutable';
end;
$$;

create trigger validation_events_immutable_update
before update or delete on validation_events
for each row execute function reject_validation_event_mutation();
