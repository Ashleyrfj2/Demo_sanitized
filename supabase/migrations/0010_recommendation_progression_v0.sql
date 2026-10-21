-- 0010_recommendation_progression_v0.sql
-- M5B review hardening: backend-owned immutable recommendation progression.

alter table recommendation_events
  add column progression_snapshot text,
  add column override_alternatives jsonb not null default '[]'::jsonb,
  add column promoted_by_response_id text references recommendation_responses(id);

-- Claim-level recommendation rows are append-only under 0008. Temporarily
-- remove the trigger only for the deterministic legacy backfill, then restore
-- the same immutable boundary below.
drop trigger if exists recommendation_events_m5a_immutable on recommendation_events;

update recommendation_events
set progression_snapshot =
  md5('legacy-recommendation-progression-v0|' || id)
  || md5('legacy-recommendation-progression-v0-secondary|' || id)
where claim_id is not null and progression_snapshot is null;

alter table recommendation_events
  add constraint recommendation_events_progression_complete check (
    claim_id is null
    or (
      progression_snapshot ~ '^[a-f0-9]{64}$'
      and jsonb_typeof(override_alternatives) = 'array'
    )
  );

drop index recommendation_events_m5a_snapshot_unique;

create unique index recommendation_events_progression_step_unique
  on recommendation_events (
    run_id, tenant_id, build_id, environment_id, actor_id, role,
    policy_version, evidence_snapshot, progression_snapshot,
    coalesce(scenario_id, ''), coalesce(device, ''), coalesce(region, ''),
    feature_flags
  )
  where claim_id is not null;

create index recommendation_events_progression_lookup_idx
  on recommendation_events (
    run_id, tenant_id, build_id, environment_id, actor_id, role,
    policy_version, evidence_snapshot, progression_snapshot
  )
  where claim_id is not null;

create index recommendation_events_promoted_response_idx
  on recommendation_events (promoted_by_response_id)
  where promoted_by_response_id is not null;

create or replace function validate_recommendation_promotion()
returns trigger
language plpgsql
as $$
declare
  source record;
begin
  if new.promoted_by_response_id is null then
    return new;
  end if;

  select response.response,
         response.override_claim_id,
         prior.run_id,
         prior.tenant_id,
         prior.build_id,
         prior.environment_id,
         prior.actor_id,
         prior.role,
         prior.policy_version,
         prior.evidence_snapshot,
         prior.scenario_id,
         prior.device,
         prior.region,
         prior.feature_flags,
         prior.progression_snapshot
    into source
  from recommendation_responses response
  join recommendation_events prior
    on prior.id = response.recommendation_id
   and prior.run_id = response.run_id
  where response.id = new.promoted_by_response_id;

  if not found
     or source.response <> 'override'
     or source.override_claim_id <> new.claim_id
     or source.run_id <> new.run_id
     or source.tenant_id <> new.tenant_id
     or source.build_id <> new.build_id
     or source.environment_id <> new.environment_id
     or source.actor_id <> new.actor_id
     or source.role <> new.role
     or source.policy_version <> new.policy_version
     or source.evidence_snapshot <> new.evidence_snapshot
     or source.scenario_id is distinct from new.scenario_id
     or source.device is distinct from new.device
     or source.region is distinct from new.region
     or source.feature_flags is distinct from new.feature_flags
     or source.progression_snapshot = new.progression_snapshot then
    raise exception using errcode = 'P0001', message = 'recommendation_promotion_invalid';
  end if;

  return new;
end;
$$;

drop trigger if exists recommendation_events_validate_promotion on recommendation_events;
create trigger recommendation_events_validate_promotion
before insert on recommendation_events
for each row execute function validate_recommendation_promotion();

drop trigger if exists recommendation_events_m5a_immutable on recommendation_events;
create trigger recommendation_events_m5a_immutable
before update or delete on recommendation_events
for each row execute function reject_m5a_recommendation_mutation();
