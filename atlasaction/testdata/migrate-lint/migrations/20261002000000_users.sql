-- Add column "email" to table: "users"
ALTER TABLE `users` ADD COLUMN `email` text NULL;
-- Create index "users_email" to table: "users"
CREATE UNIQUE INDEX `users_email` ON `users` (`email`);
-- Create index "orders_user_id" to table: "orders"
CREATE INDEX `orders_user_id` ON `orders` (`user_id`);
-- Create "order_notes" table
CREATE TABLE `order_notes` (
  `id` integer NOT NULL,
  `order_id` integer NOT NULL,
  `body` text NOT NULL,
  PRIMARY KEY (`id`)
);
