CREATE TABLE `users` (
  `id` integer NOT NULL,
  `name` text NULL,
  PRIMARY KEY (`id`)
);
CREATE TABLE `orders` (
  `id` integer NOT NULL,
  `user_id` integer NOT NULL,
  `status` text NOT NULL,
  `total` integer NOT NULL,
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
  `retries` integer NOT NULL DEFAULT 0,
  `processed_at` text NULL,
  `created_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
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
CREATE TABLE `exports` (
  `id` integer NOT NULL,
  PRIMARY KEY (`id`)
);
