-- 0001_initial.down.sql — drop core tables (keeps chunks for migration 0002).

DROP TRIGGER IF EXISTS nodes_au;
DROP TRIGGER IF EXISTS nodes_ad;
DROP TRIGGER IF EXISTS nodes_ai;
DROP TABLE IF EXISTS nodes_fts;
DROP TABLE IF EXISTS edges;
DROP TABLE IF EXISTS nodes;
