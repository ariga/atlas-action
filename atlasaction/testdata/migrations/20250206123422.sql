-- atlas:txtar

-- checks/destructive.sql --
-- atlas:assert DS103
SELECT NOT EXISTS (SELECT 1 FROM `t1` WHERE `c2` IS NOT NULL) AS `is_empty`;

-- migration.sql --
alter table t1 drop column c2;
