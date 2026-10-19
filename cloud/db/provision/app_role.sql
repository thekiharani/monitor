\set ON_ERROR_STOP on

CREATE ROLE monitor_app
    LOGIN
    PASSWORD :'app_password'
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    NOBYPASSRLS
    NOREPLICATION
    CONNECTION LIMIT 40;

GRANT CONNECT ON DATABASE monitor_core TO monitor_app;

\connect monitor_core

REVOKE ALL ON SCHEMA public FROM monitor_app;
GRANT USAGE ON SCHEMA public TO monitor_app;

SELECT rolname, rolsuper, rolbypassrls, rolcreatedb, rolcreaterole, rolreplication
FROM pg_roles WHERE rolname = 'monitor_app';
