CREATE TABLE `users` (
  `id` integer NOT NULL,
  `name` text NOT NULL,
  PRIMARY KEY (`id`)
);
CREATE TABLE `orders` (
  `id` integer NOT NULL,
  `user_id` integer NOT NULL,
  `status` text NOT NULL,
  `total` integer NOT NULL,
  `updated_at` text NULL,
  PRIMARY KEY (`id`)
);
CREATE TABLE `order_events` (
  `id` integer NOT NULL,
  `order_id` integer NOT NULL,
  `kind` text NOT NULL,
  `actor` text NULL,
  `source` text NULL,
  `channel` text NULL,
  `ip_address` text NULL,
  `user_agent` text NULL,
  `request_id` text NULL,
  `session_id` text NULL,
  `previous_status` text NULL,
  `next_status` text NULL,
  `amount` integer NULL,
  `currency` text NULL,
  `reason` text NULL,
  `note` text NULL,
  `payload` text NULL,
  `retries` integer NOT NULL DEFAULT 3,
  `processed_at` text NULL,
  `created_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
CREATE TABLE `order_totals` (
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
CREATE TABLE `order_notes` (
  `id` integer NOT NULL,
  `order_id` integer NOT NULL,
  `body` text NOT NULL,
  PRIMARY KEY (`id`)
);
