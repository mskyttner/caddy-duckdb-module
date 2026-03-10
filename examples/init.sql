-- Attach the DiVA OAI normalized database in read-only mode.
-- The file is bind-mounted into the container at /data/diva/.
ATTACH '/data/diva/diva_oai_normalized.db' AS diva (READ_ONLY);

-- Set diva as the default catalog for this connection so that
-- unqualified table names (e.g. SELECT * FROM publications) resolve
-- to diva tables without needing the diva. prefix.
USE diva;
