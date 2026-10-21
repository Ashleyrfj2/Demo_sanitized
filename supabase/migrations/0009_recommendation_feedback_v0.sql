-- 0009_recommendation_feedback_v0.sql
-- M5B: immutable accept/dismiss/override feedback with explicit alternative claim identity.

alter table recommendation_responses
  add column override_claim_id text references claim_definitions(id);

alter table recommendation_responses
  drop constraint if exists recommendation_responses_response_check;

alter table recommendation_responses
  add constraint recommendation_responses_response_check
  check (response in ('accepted', 'dismissed', 'override')),
  add constraint recommendation_responses_override_shape_check
  check (
    (response = 'override' and override_claim_id is not null and btrim(override_claim_id) <> '')
    or (response in ('accepted', 'dismissed') and override_claim_id is null)
  );

create index recommendation_responses_override_claim_idx
  on recommendation_responses (override_claim_id)
  where override_claim_id is not null;

create or replace function validate_recommendation_response_override()
returns trigger
language plpgsql
as $$
declare
  recommendation_tenant text;
  recommended_claim text;
  override_tenant text;
begin
  if new.response <> 'override' then
    return new;
  end if;

  select tenant_id, claim_id
    into recommendation_tenant, recommended_claim
  from recommendation_events
  where id = new.recommendation_id and run_id = new.run_id;

  if not found or recommended_claim is null then
    raise exception using errcode = 'P0001', message = 'override_claim_invalid';
  end if;

  select tenant_id into override_tenant
  from claim_definitions
  where id = new.override_claim_id;

  if not found
     or override_tenant <> recommendation_tenant
     or new.override_claim_id = recommended_claim then
    raise exception using errcode = 'P0001', message = 'override_claim_invalid';
  end if;

  return new;
end;
$$;

drop trigger if exists recommendation_responses_validate_override on recommendation_responses;
create trigger recommendation_responses_validate_override
before insert on recommendation_responses
for each row execute function validate_recommendation_response_override();

create or replace function reject_recommendation_response_mutation()
returns trigger
language plpgsql
as $$
begin
  raise exception 'recommendation feedback is immutable';
end;
$$;

drop trigger if exists recommendation_responses_immutable on recommendation_responses;
create trigger recommendation_responses_immutable
before update or delete on recommendation_responses
for each row execute function reject_recommendation_response_mutation();
