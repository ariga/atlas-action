-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Drop "t1" table
DROP TABLE `t1`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
