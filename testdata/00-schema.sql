-- Databases, users and grants for the local sqit test environment.
-- Security invariant: sqit_upstream_user can only SELECT.

DROP DATABASE IF EXISTS sqit_local;
DROP DATABASE IF EXISTS sqit_upstream;
CREATE DATABASE sqit_local    CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE sqit_upstream CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE USER IF NOT EXISTS 'sqit_local_user'@'%'    IDENTIFIED BY 'sqit_local_pw';
CREATE USER IF NOT EXISTS 'sqit_upstream_user'@'%' IDENTIFIED BY 'sqit_upstream_pw';
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'sqit_local_user'@'%', 'sqit_upstream_user'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE ON sqit_local.* TO 'sqit_local_user'@'%';
GRANT SELECT                         ON sqit_upstream.* TO 'sqit_upstream_user'@'%';
FLUSH PRIVILEGES;

-- Identical schema in both databases.

CREATE TABLE sqit_local.users (
  id     INT          NOT NULL,
  name   VARCHAR(100) NOT NULL,
  email  VARCHAR(100) NULL,
  active TINYINT(1)   NOT NULL DEFAULT 1,
  PRIMARY KEY (id)
) ENGINE=InnoDB;

CREATE TABLE sqit_local.orders (
  customer_id INT           NOT NULL,
  order_id    INT           NOT NULL,
  total       DECIMAL(10,2) NOT NULL,
  note        VARCHAR(200)  NULL,
  PRIMARY KEY (customer_id, order_id)
) ENGINE=InnoDB;

CREATE TABLE sqit_upstream.users  LIKE sqit_local.users;
CREATE TABLE sqit_upstream.orders LIKE sqit_local.orders;
