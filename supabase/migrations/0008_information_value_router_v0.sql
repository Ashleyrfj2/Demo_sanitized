-- 0008_information_value_router_v0.sql
-- M5A: immutable, claim-level information-value routing decisions.

alter table claim_definitions
  add constraint claim_definitions_id_tenant_unique unique (id, tenant_id);

alter table recommendation_events
  add column claim_id text,
  add column scenario_id text,
  add column device text,
  add column region text,
  add column classification text,
  add column max_verification_depth integer,
  add column independent_source_count integer,
  add column conflict_count integer,
  add column policy_version text,
  add column evidence_snapshot text,
  add column feature_flags jsonb not null default '{}'::jsonb;

alter table recommendation_events
  add constraint recommendation_events_claim_tenant_fk
  foreign key (claim_id, tenant_id) references claim_definitions (id, tenant_id),
  add constraint recommendation_events_m5a_complete check (
    claim_id is null
    or (
      candidate_state_id is null
      and policy_version is not null and btrim(policy_version) <> ''
      and evidence_snapshot ~ '^[a-f0-9]{64}$'
      and classification in ('Solid', 'Weak', 'Stale', 'Conflicted', 'Untouched', 'Blocked')
      and max_verification_depth between 0 and 4
      and independent_source_count >= 0
      and conflict_count >= 0
      and jsonb_typeof(feature_flags) = 'object'
      and factors ?& array[
        'delta_uncertainty', 'business_risk', 'diff_impact',
        'smoothed_failure_prior', 'expected_cost'
      ]
      and score >= 0
      and score::text not in ('NaN', 'Infinity', '-Infinity')
    )
  );

create unique index recommendation_events_m5a_snapshot_unique
  on recommendation_events (
    run_id, tenant_id, build_id, environment_id, actor_id, role,
    policy_version, evidence_snapshot,
    coalesce(scenario_id, ''), coalesce(device, ''), coalesce(region, ''),
    feature_flags
  )
  where claim_id is not null;

create index recommendation_events_claim_time_idx
  on recommendation_events (claim_id, created_at, id)
  where claim_id is not null;

create or replace function reject_m5a_recommendation_mutation()
returns trigger
language plpgsql
as $$
begin
  if old.claim_id is not null then
    raise exception 'claim-level recommendation decisions are immutable';
  end if;
  return case when tg_op = 'DELETE' then old else new end;
end;
$$;

drop trigger if exists recommendation_events_m5a_immutable on recommendation_events;
create trigger recommendation_events_m5a_immutable
before update or delete on recommendation_events
for each row execute function reject_m5a_recommendation_mutation();
