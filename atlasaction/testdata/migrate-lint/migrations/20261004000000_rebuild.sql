-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_users" table
CREATE TABLE `new_users` (
  `id` integer NOT NULL,
  `name` text NOT NULL,
  `email` text NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "users" to new temporary table "new_users"
INSERT INTO `new_users` (`id`, `name`, `email`) SELECT `id`, `name`, `email` FROM `users`;
-- Drop "users" table after copying rows
DROP TABLE `users`;
-- Rename temporary table "new_users" to "users"
ALTER TABLE `new_users` RENAME TO `users`;
-- Create index "users_email" to table: "users"
CREATE UNIQUE INDEX `users_email` ON `users` (`email`);
-- Create "new_order_totals" table
CREATE TABLE `new_order_totals` (
  `order_id` integer NOT NULL,
  `total_x1` real NOT NULL,
  `total_x2` real NOT NULL,
  `total_x3` real NOT NULL,
  `total_x4` real NOT NULL,
  `total_x5` real NOT NULL,
  `total_x6` real NOT NULL,
  `total_x7` real NOT NULL,
  `total_x8` real NOT NULL,
  `total_x9` real NOT NULL,
  `total_x10` real NOT NULL,
  `total_x11` real NOT NULL,
  `total_x12` real NOT NULL,
  `total_x13` real NOT NULL,
  `total_x14` real NOT NULL,
  `total_x15` real NOT NULL,
  `total_x16` real NOT NULL,
  `total_x17` real NOT NULL,
  `total_x18` real NOT NULL,
  `total_x19` real NOT NULL,
  `total_x20` real NOT NULL,
  `total_x21` real NOT NULL,
  `total_x22` real NOT NULL,
  `total_x23` real NOT NULL,
  `total_x24` real NOT NULL,
  PRIMARY KEY (`order_id`)
);
-- Copy rows from old table "order_totals" to new temporary table "new_order_totals"
INSERT INTO `new_order_totals` (`order_id`, `total_x1`, `total_x2`, `total_x3`, `total_x4`, `total_x5`, `total_x6`, `total_x7`, `total_x8`, `total_x9`, `total_x10`, `total_x11`, `total_x12`, `total_x13`, `total_x14`, `total_x15`, `total_x16`, `total_x17`, `total_x18`, `total_x19`, `total_x20`, `total_x21`, `total_x22`, `total_x23`, `total_x24`) SELECT `order_id`, `total_x1`, `total_x2`, `total_x3`, `total_x4`, `total_x5`, `total_x6`, `total_x7`, `total_x8`, `total_x9`, `total_x10`, `total_x11`, `total_x12`, `total_x13`, `total_x14`, `total_x15`, `total_x16`, `total_x17`, `total_x18`, `total_x19`, `total_x20`, `total_x21`, `total_x22`, `total_x23`, `total_x24` FROM `order_totals`;
-- Drop "order_totals" table after copying rows
DROP TABLE `order_totals`;
-- Rename temporary table "new_order_totals" to "order_totals"
ALTER TABLE `new_order_totals` RENAME TO `order_totals`;
-- Drop "exports" table
DROP TABLE `exports`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
