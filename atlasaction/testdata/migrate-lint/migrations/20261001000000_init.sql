-- Create "users" table
CREATE TABLE `users` (
  `id` integer NOT NULL,
  `name` text NULL,
  PRIMARY KEY (`id`)
);
-- Create "products" table
CREATE TABLE `products` (
  `id` integer NOT NULL,
  `sku` text NOT NULL,
  `name` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "products_sku" to table: "products"
CREATE UNIQUE INDEX `products_sku` ON `products` (`sku`);
-- Create "orders" table
CREATE TABLE `orders` (
  `id` integer NOT NULL,
  `user_id` integer NOT NULL,
  `total` integer NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "orders_total" to table: "orders"
CREATE INDEX `orders_total` ON `orders` (`total`);
-- Create "order_items" table
CREATE TABLE `order_items` (
  `id` integer NOT NULL,
  `order_id` integer NOT NULL,
  `sku` text NOT NULL,
  `quantity` integer NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "order_items_order_id" to table: "order_items"
CREATE INDEX `order_items_order_id` ON `order_items` (`order_id`);
-- Create index "order_items_sku" to table: "order_items"
CREATE INDEX `order_items_sku` ON `order_items` (`sku`);
-- Create "order_totals" table
CREATE TABLE `order_totals` (
  `order_id` integer NOT NULL,
  `total_x1` integer NOT NULL,
  `total_x2` integer NOT NULL,
  `total_x3` integer NOT NULL,
  `total_x4` integer NOT NULL,
  `total_x5` integer NOT NULL,
  `total_x6` integer NOT NULL,
  `total_x7` integer NOT NULL,
  `total_x8` integer NOT NULL,
  `total_x9` integer NOT NULL,
  `total_x10` integer NOT NULL,
  `total_x11` integer NOT NULL,
  `total_x12` integer NOT NULL,
  `total_x13` integer NOT NULL,
  `total_x14` integer NOT NULL,
  `total_x15` integer NOT NULL,
  `total_x16` integer NOT NULL,
  `total_x17` integer NOT NULL,
  `total_x18` integer NOT NULL,
  `total_x19` integer NOT NULL,
  `total_x20` integer NOT NULL,
  `total_x21` integer NOT NULL,
  `total_x22` integer NOT NULL,
  `total_x23` integer NOT NULL,
  `total_x24` integer NOT NULL,
  PRIMARY KEY (`order_id`)
);
-- Create "exports" table
CREATE TABLE `exports` (
  `id` integer NOT NULL,
  `name` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "exports_name" to table: "exports"
CREATE INDEX `exports_name` ON `exports` (`name`);
