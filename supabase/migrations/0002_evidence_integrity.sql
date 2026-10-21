alter table state_corrections
  add column if not exists build_id text,
  add column if not exists environment_id text;

update state_corrections corrections
set build_id = states.build_id,
    environment_id = states.environment_id
from candidate_states states
where states.id = corrections.state_id
  and (corrections.build_id is null or corrections.environment_id is null);

alter table state_corrections
  alter column build_id set not null,
  alter column environment_id set not null;
