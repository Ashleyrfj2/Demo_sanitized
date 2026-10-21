create table build_registry (
  tenant_id text not null references tenants(id),
  environment_id text not null,
  build_id text not null,
  sequence_number bigint not null check (sequence_number > 0),
  predecessor_build_id text,
  registered_by text not null,
  registered_at timestamptz not null default now(),
  primary key (tenant_id, environment_id, build_id),
  unique (tenant_id, environment_id, sequence_number),
  check (
    (sequence_number = 1 and predecessor_build_id is null)
    or (sequence_number > 1 and predecessor_build_id is not null)
  ),
  foreign key (tenant_id, environment_id, predecessor_build_id)
    references build_registry (tenant_id, environment_id, build_id)
);

create index build_registry_latest_idx
  on build_registry (tenant_id, environment_id, sequence_number desc);

do $$
begin
  if exists (
    select 1
    from validation_events
    group by tenant_id, environment_id
    having count(distinct build_id) > 1
  ) then
    raise exception 'cannot backfill build_registry: an evidence scope contains multiple unordered builds';
  end if;
end;
$$;

insert into build_registry (
  tenant_id, environment_id, build_id, sequence_number,
  predecessor_build_id, registered_by, registered_at
)
select tenant_id, environment_id, min(build_id), 1, null,
       'migration-backfill', min(created_at)
from validation_events
group by tenant_id, environment_id;

create function validate_build_registry_chain()
returns trigger language plpgsql as $$
declare
  predecessor_sequence bigint;
begin
  if new.sequence_number = 1 then
    if new.predecessor_build_id is not null then
      raise exception 'the first build cannot name a predecessor';
    end if;
    return new;
  end if;

  select sequence_number into predecessor_sequence
  from build_registry
  where tenant_id = new.tenant_id
    and environment_id = new.environment_id
    and build_id = new.predecessor_build_id;

  if predecessor_sequence is null or predecessor_sequence <> new.sequence_number - 1 then
    raise exception 'build predecessor must be the immediately preceding registered build';
  end if;
  return new;
end;
$$;

create trigger build_registry_validate_chain
before insert on build_registry
for each row execute function validate_build_registry_chain();

create function reject_build_registry_mutation()
returns trigger language plpgsql as $$
begin
  raise exception 'build_registry is immutable';
end;
$$;

create trigger build_registry_immutable_update
before update or delete on build_registry
for each row execute function reject_build_registry_mutation();
