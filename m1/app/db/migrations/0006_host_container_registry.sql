-- +goose Up
-- Multi-host fleet registry + per-user container mapping.
-- Designed for horizontal scale: many worker hosts, one row per host; the
-- gateway routes each user to their container's host:port via the containers table.

-- Worker hosts (M2 today; M3/M4/... on cloud later). Each runs a control-agent.
CREATE TABLE IF NOT EXISTS hosts (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,          -- e.g. 'm2', 'worker-eu-1'
    control_url  TEXT NOT NULL,                 -- control-agent base, e.g. http://10.152.152.100:9090
    internal_ip  TEXT NOT NULL,                 -- where containers are reachable, e.g. 10.152.152.100
    capacity     INT  NOT NULL DEFAULT 50,      -- max containers this host should hold
    active       BOOLEAN NOT NULL DEFAULT true,  -- false = drain, don't place new containers
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One container per user. status drives the lifecycle; host_id+port is the route target.
CREATE TABLE IF NOT EXISTS containers (
    user_id        BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    host_id        BIGINT NOT NULL REFERENCES hosts(id),
    container_name TEXT NOT NULL,               -- e.g. 'oc-user-42'
    port           INT  NOT NULL,               -- host port the container's OpenCode listens on
    status         TEXT NOT NULL DEFAULT 'stopped', -- stopped | starting | running | error
    last_active    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (host_id, port),
    UNIQUE (host_id, container_name)
);
CREATE INDEX IF NOT EXISTS idx_containers_host ON containers(host_id);
CREATE INDEX IF NOT EXISTS idx_containers_status ON containers(status);
CREATE INDEX IF NOT EXISTS idx_containers_last_active ON containers(last_active);

-- Seed M2 as the first host. control_url points at the control-agent we build next
-- (port 9090). internal_ip is where the per-user containers are reachable.
INSERT INTO hosts (name, control_url, internal_ip, capacity, active)
VALUES ('m2', 'http://10.152.152.100:9090', '10.152.152.100', 50, true)
ON CONFLICT (name) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS containers;
DROP TABLE IF EXISTS hosts;
