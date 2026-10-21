-- Gate 2 makes experiment runs explicit, partitions every derived entity by
-- run_id, and gives PostgreSQL the final say on whether a run is writable.

do $$
begin
  if exists (select 1 from validation_events where run_id is null or btrim(run_id) = '') then
    raise exception '0005 migration cannot backfill validation_events without run_id';
  end if;
  if exists (
    select 1
    from validation_events events
    left join experiment_runs runs on runs.id = events.run_id
    where runs.id is null
       or runs.tenant_id <> events.tenant_id
       or runs.build_id <> events.build_id
       or runs.environment_id <> events.environment_id
       or coalesce(runs.scenario_id, '') <> coalesce(events.context->>'scenario_id', '')
  ) then
    raise exception '0005 migration found validation_events with missing or mismatched runs';
  end if;
  if exists (
    select states.id
    from candidate_states states
    left join evidence_records evidence on evidence.candidate_state_id = states.id
    left join validation_events events on events.event_id = evidence.event_id
    group by states.id
    having count(distinct events.run_id) <> 1
  ) then
    raise exception '0005 migration cannot unambiguously backfill candidate_states run_id';
  end if;
end;
$$;

alter table experiment_runs
  add column if not exists created_by text;

update experiment_runs set created_by = 'migration-backfill' where created_by is null;

alter table experiment_runs
  alter column created_by set not null,
  alter column scenario_id set not null;

alter table experiment_runs drop constraint if exists experiment_runs_mode_check;
alter table experiment_runs
  add constraint experiment_runs_mode_check check (mode in ('baseline', 'guided'));

alter table experiment_runs
  add constraint experiment_runs_registered_build_fk
  foreign key (tenant_id, environment_id, build_id)
  references build_registry (tenant_id, environment_id, build_id);

alter table candidate_states add column if not exists run_id text;
update candidate_states states
set run_id = inferred.run_id
from (
  select evidence.candidate_state_id, min(events.run_id) as run_id
  from evidence_records evidence
  join validation_events events on events.event_id = evidence.event_id
  group by evidence.candidate_state_id
) inferred
where inferred.candidate_state_id = states.id and states.run_id is null;

alter table evidence_records add column if not exists run_id text;
update evidence_records evidence
set run_id = events.run_id
from validation_events events
where events.event_id = evidence.event_id and evidence.run_id is null;

alter table state_corrections add column if not exists run_id text;
update state_corrections corrections
set run_id = states.run_id
from candidate_states states
where states.id = corrections.state_id and corrections.run_id is null;

alter table verification_requests add column if not exists run_id text;
update verification_requests requests
set run_id = states.run_id
from candidate_states states
where states.id = requests.candidate_state_id and requests.run_id is null;

update recommendation_events recommendations
set run_id = states.run_id
from candidate_states states
where states.id = recommendations.candidate_state_id and recommendations.run_id is null;

alter table recommendation_responses add column if not exists run_id text;
update recommendation_responses responses
set run_id = recommendations.run_id
from recommendation_events recommendations
where recommendations.id = responses.recommendation_id and responses.run_id is null;

do $$
begin
  if exists (select 1 from candidate_states where run_id is null)
     or exists (select 1 from evidence_records where run_id is null)
     or exists (select 1 from state_corrections where run_id is null)
     or exists (select 1 from verification_requests where run_id is null)
     or exists (select 1 from recommendation_events where run_id is null)
     or exists (select 1 from recommendation_responses where run_id is null) then
    raise exception '0005 migration cannot unambiguously backfill all run-attributed entities';
  end if;
  if exists (
    select 1 from recommendation_events recommendations
    join candidate_states states on states.id = recommendations.candidate_state_id
    where recommendations.run_id <> states.run_id
  ) or exists (
    select 1 from recommendation_responses responses
    join recommendation_events recommendations on recommendations.id = responses.recommendation_id
    where responses.run_id <> recommendations.run_id
  ) then
    raise exception '0005 migration found cross-run recommendation relationships';
  end if;
end;
$$;

alter table validation_events alter column run_id set not null;
alter table candidate_states alter column run_id set not null;
alter table evidence_records alter column run_id set not null;
alter table state_corrections alter column run_id set not null;
alter table verification_requests alter column run_id set not null;
alter table recommendation_events alter column run_id set not null;
alter table recommendation_responses alter column run_id set not null;

do $$
begin
  if exists (
    select run_id, recommendation_id
    from recommendation_responses
    group by run_id, recommendation_id
    having count(*) > 1
  ) then
    raise exception '0005 migration found multiple responses for one run recommendation';
  end if;
end;
$$;

alter table recommendation_responses
  add constraint recommendation_responses_run_recommendation_unique
  unique (run_id, recommendation_id);

do $$
declare
  constraint_name text;
begin
  for constraint_name in
    select constraint_data.conname
    from pg_constraint constraint_data
    where constraint_data.conrelid = 'candidate_states'::regclass
      and constraint_data.contype = 'u'
      and pg_get_constraintdef(constraint_data.oid) like '%tenant_id, build_id, environment_id, role, state_fingerprint%'
      and pg_get_constraintdef(constraint_data.oid) not like '%run_id%'
  loop
    execute format('alter table candidate_states drop constraint %I', constraint_name);
  end loop;
end;
$$;

alter table candidate_states
  add constraint candidate_states_run_scope_fingerprint_unique
  unique (run_id, tenant_id, build_id, environment_id, role, state_fingerprint);

alter table validation_events
  add constraint validation_events_run_fk foreign key (run_id) references experiment_runs(id);
alter table candidate_states
  add constraint candidate_states_run_fk foreign key (run_id) references experiment_runs(id);
alter table evidence_records
  add constraint evidence_records_run_fk foreign key (run_id) references experiment_runs(id);
alter table state_corrections
  add constraint state_corrections_run_fk foreign key (run_id) references experiment_runs(id);
alter table verification_requests
  add constraint verification_requests_run_fk foreign key (run_id) references experiment_runs(id);
alter table recommendation_responses
  add constraint recommendation_responses_run_fk foreign key (run_id) references experiment_runs(id);

alter table candidate_states add constraint candidate_states_id_run_unique unique (id, run_id);
alter table validation_events add constraint validation_events_id_run_unique unique (event_id, run_id);
alter table recommendation_events add constraint recommendation_events_id_run_unique unique (id, run_id);

alter table evidence_records
  add constraint evidence_records_candidate_run_fk
  foreign key (candidate_state_id, run_id) references candidate_states (id, run_id),
  add constraint evidence_records_event_run_fk
  foreign key (event_id, run_id) references validation_events (event_id, run_id);
alter table verification_requests
  add constraint verification_requests_candidate_run_fk
  foreign key (candidate_state_id, run_id) references candidate_states (id, run_id);
alter table recommendation_events
  add constraint recommendation_events_candidate_run_fk
  foreign key (candidate_state_id, run_id) references candidate_states (id, run_id);
alter table recommendation_responses
  add constraint recommendation_responses_recommendation_run_fk
  foreign key (recommendation_id, run_id) references recommendation_events (id, run_id);

drop index if exists candidate_states_scope_status_idx;
create index candidate_states_run_scope_status_idx
  on candidate_states (run_id, tenant_id, build_id, environment_id, status);
create index evidence_records_run_candidate_time_idx
  on evidence_records (run_id, candidate_state_id, occurred_at);
create index verification_requests_run_status_idx
  on verification_requests (run_id, status);
create index recommendation_events_run_actor_time_idx
  on recommendation_events (run_id, actor_id, created_at, id);
create index recommendation_responses_run_recommendation_idx
  on recommendation_responses (run_id, recommendation_id);
create index state_corrections_run_time_idx
  on state_corrections (run_id, created_at, id);

alter table experiment_metrics
  add column if not exists canonical_input jsonb,
  add column if not exists input_receipt jsonb,
  add column if not exists snapshot jsonb;

alter table experiment_metrics
  add constraint experiment_metrics_v11_receipt_complete check (
    definition_version <> 'festival-v1.1'
    or (canonical_input is not null and input_receipt is not null and snapshot is not null)
  );

-- No search_path pin here. The body declares experiment_runs%rowtype, which the
-- plpgsql validator resolves at CREATE FUNCTION time; pinning to public breaks
-- any deployment whose tables are not in public, including the isolated-schema
-- integration harness. This function is not SECURITY DEFINER, so it runs with
-- the invoker's privileges and search_path.
create or replace function assert_run_attributed_write_is_open()
returns trigger
language plpgsql
as $$
declare
  row_data jsonb := case when tg_op = 'DELETE' then to_jsonb(old) else to_jsonb(new) end;
  target_run experiment_runs%rowtype;
  target_run_id text := row_data->>'run_id';
  row_context jsonb;
begin
  if tg_op = 'UPDATE' and old.run_id is distinct from new.run_id then
    raise exception using errcode = 'P0001', message = 'run_scope_mismatch';
  end if;
  if target_run_id is null or btrim(target_run_id) = '' then
    raise exception using errcode = 'P0001', message = 'unknown_run';
  end if;
  select * into target_run from experiment_runs where id = target_run_id for update;
  if not found then
    raise exception using errcode = 'P0001', message = 'unknown_run';
  end if;
  if target_run.frozen_at is not null then
    raise exception using errcode = 'P0001', message = 'run_frozen';
  end if;
  if row_data ? 'tenant_id' and row_data->>'tenant_id' <> target_run.tenant_id then
    raise exception using errcode = 'P0001', message = 'run_scope_mismatch';
  end if;
  if row_data ? 'build_id' and row_data->>'build_id' <> target_run.build_id then
    raise exception using errcode = 'P0001', message = 'run_scope_mismatch';
  end if;
  if row_data ? 'environment_id' and row_data->>'environment_id' <> target_run.environment_id then
    raise exception using errcode = 'P0001', message = 'run_scope_mismatch';
  end if;
  if tg_table_name = 'validation_events' then
    row_context := coalesce(row_data->'context', '{}'::jsonb);
    if coalesce(row_context->>'scenario_id', '') <> target_run.scenario_id then
      raise exception using errcode = 'P0001', message = 'run_scope_mismatch';
    end if;
  end if;
  return case when tg_op = 'DELETE' then old else new end;
end;
$$;

do $$
declare
  table_name text;
begin
  foreach table_name in array array[
    'validation_events', 'candidate_states', 'evidence_records',
    'state_corrections', 'verification_requests', 'recommendation_events',
    'recommendation_responses'
  ] loop
    execute format('drop trigger if exists %I on %I', table_name || '_writable_run', table_name);
    execute format(
      'create trigger %I before insert or update or delete on %I for each row execute function assert_run_attributed_write_is_open()',
      table_name || '_writable_run', table_name
    );
  end loop;
end;
$$;

drop trigger if exists experiment_metrics_writable_run on experiment_metrics;
create trigger experiment_metrics_writable_run
before insert on experiment_metrics
for each row execute function assert_run_attributed_write_is_open();

create or replace function protect_experiment_run_identity()
returns trigger
language plpgsql
as $$
begin
  if old.id <> new.id
     or old.tenant_id <> new.tenant_id
     or old.build_id <> new.build_id
     or old.environment_id <> new.environment_id
     or old.scenario_id <> new.scenario_id
     or old.mode <> new.mode
     or old.started_at <> new.started_at
     or old.created_by <> new.created_by
     or old.frozen_at is not null
     or new.frozen_at is null then
    raise exception 'experiment run identity and lifecycle are immutable';
  end if;
  return new;
end;
$$;

drop trigger if exists experiment_runs_immutable_identity on experiment_runs;
create trigger experiment_runs_immutable_identity
before update on experiment_runs
for each row execute function protect_experiment_run_identity();

create or replace function reject_experiment_snapshot_mutation()
returns trigger
language plpgsql
as $$
begin
  raise exception 'experiment metric snapshots are immutable';
end;
$$;

drop trigger if exists experiment_metrics_immutable_update on experiment_metrics;
create trigger experiment_metrics_immutable_update
before update or delete on experiment_metrics
for each row execute function reject_experiment_snapshot_mutation();
