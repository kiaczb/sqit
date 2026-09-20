-- Seed data for the UPSTREAM database (plays the TEST role). Same as local initially.
INSERT INTO sqit_upstream.users (id, name, email, active) VALUES
  (1,  'Anna',  'anna@example.com',  1),
  (2,  'John',  'john@example.com',  1),
  (3,  'Maria', NULL,                0),
  (42, 'Peter', 'old@example.com',   1);

INSERT INTO sqit_upstream.orders (customer_id, order_id, total, note) VALUES
  (1,  1, 10.00, 'first order'),
  (1,  2, 25.50, NULL),
  (42, 1, 99.99, 'gift');
