-- 0007_reconciliation_v0.sql
-- M4C: Evidence Reconciliation V0 Schema & Forward Repair

create table if not exists claim_definitions (
  id text primary key,
  tenant_id text not null references tenants(id),
  title text not null,
  description text,
  target text not null,
  state_signature text not null,
  actor_context text not null,
  validation_intent text not null,
  created_at timestamptz not null default now()
);

create index if not exists claim_definitions_tenant_target_idx
  on claim_definitions(tenant_id, target);

create table if not exists evidence_contexts (
  id text not null,
  tenant_id text not null references tenants(id),
  run_id text not null references experiment_runs(id),
  build_id text not null,
  environment_id text not null,
  scenario_id text,
  feature_flags jsonb default '{}'::jsonb,
  device text,
  region text,
  created_at timestamptz not null default now(),
  primary key (id, run_id),
  foreign key (tenant_id, environment_id, build_id) references build_registry(tenant_id, environment_id, build_id)
);

create index if not exists evidence_contexts_run_idx
  on evidence_contexts(run_id);

create table if not exists observations (
  id text primary key,
  tenant_id text not null references tenants(id),
  run_id text not null references experiment_runs(id),
  claim_id text not null references claim_definitions(id),
  evidence_context_id text not null,
  event_id text,
  source source_type not null,
  source_adapter text not null,
  actor_id text not null,
  assessment text not null,
  verifier_result text,
  occurred_at timestamptz not null,
  created_at timestamptz not null default now(),
  foreign key (evidence_context_id, run_id) references evidence_contexts(id, run_id),
  foreign key (event_id, run_id) references validation_events(event_id, run_id)
);

-- Explicitly normalize assessment check constraint for both fresh and pre-existing M4B tables
alter table observations
  drop constraint if exists observations_assessment_check;

alter table observations
  add constraint observations_assessment_check
  check (
    assessment in (
      'supports',
      'weakens',
      'conflicts',
      'invalidates',
      'blocked'
    )
  );

create index if not exists observations_claim_run_idx
  on observations(claim_id, run_id);

create index if not exists observations_context_idx
  on observations(evidence_context_id);

create index if not exists observations_event_idx
  on observations(event_id);

drop trigger if exists evidence_contexts_writable_run on evidence_contexts;
create trigger evidence_contexts_writable_run
  before insert or update or delete on evidence_contexts
  for each row execute function assert_run_attributed_write_is_open();

drop trigger if exists observations_writable_run on observations;
create trigger observations_writable_run
  before insert or update or delete on observations
  for each row execute function assert_run_attributed_write_is_open();
