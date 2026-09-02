-- Virtual callers (the connector's synthetic "user" node) must be stored so
-- blast-radius BFS can treat them as leaves the same way it treats virtual
-- servers. Without this, hop 2 walks through the shared "user" hub and pulls
-- in every other unmatched-ingress service in the cluster.
-- NOTE: keep semicolons out of comments.

ALTER TABLE service_graph_edges
    ADD COLUMN client_is_virtual UInt8 DEFAULT 0 AFTER server_is_virtual;
