SELECT 'CREATE ROLE debezium WITH REPLICATION LOGIN PASSWORD ''debezium'''
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium')
\gexec

ALTER ROLE debezium WITH REPLICATION LOGIN PASSWORD 'debezium';
GRANT CONNECT ON DATABASE crm TO debezium;
GRANT USAGE ON SCHEMA public TO debezium;
GRANT SELECT ON TABLE public.clients TO debezium;

SELECT 'CREATE PUBLICATION bionicpro_crm_publication FOR TABLE public.clients'
WHERE NOT EXISTS (
    SELECT 1 FROM pg_publication WHERE pubname = 'bionicpro_crm_publication'
)
\gexec
