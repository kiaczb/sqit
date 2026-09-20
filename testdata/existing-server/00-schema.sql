-- Same as ../00-schema.sql but for an EXISTING MySQL server (5.5 - 5.7
-- compatible syntax: GRANT ... IDENTIFIED BY creates the users; user names
-- are at most 16 characters on 5.5, hence sqit_local / sqit_upstream).
-- Only the sqit_* databases and users are touched.

DROP DATABASE IF EXISTS sqit_local;
DROP DATABASE IF EXISTS sqit_upstream;
CREATE DATABASE sqit_local    CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE sqit_upstream CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

GRANT USAGE ON *.* TO 'sqit_local'@'%'    IDENTIFIED BY 'sqit_local_pw';
GRANT USAGE ON *.* TO 'sqit_upstream'@'%' IDENTIFIED BY 'sqit_upstream_pw';
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'sqit_local'@'%';
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'sqit_upstream'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE ON sqit_local.* TO 'sqit_local'@'%';
GRANT SELECT                         ON sqit_upstream.* TO 'sqit_upstream'@'%';
FLUSH PRIVILEGES;

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
