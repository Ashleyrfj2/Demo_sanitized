-- A stable actor may be observed through multiple authenticated evidence
-- sources. Provenance belongs to each immutable validation event, not to the
-- actor registry row.
alter table actors drop column if exists source;
