-- Teams, the Keycloak groups mapped to them, and BFF sessions.

CREATE TABLE teams (
    -- name is the value of the `team` label on workloads.
    name         text PRIMARY KEY
                 CHECK (length(name) <= 63 AND name ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE team_groups (
    team_name  text NOT NULL REFERENCES teams (name) ON DELETE CASCADE,
    group_name text NOT NULL CHECK (length(group_name) BETWEEN 1 AND 255),
    PRIMARY KEY (team_name, group_name)
);

CREATE INDEX team_groups_group_name_idx ON team_groups (group_name);

-- Only the SHA-256 of the session id is stored, so a database leak does not
-- yield usable cookies. OIDC tokens are never persisted (ADR 0009).
CREATE TABLE sessions (
    id_hash      bytea PRIMARY KEY CHECK (length(id_hash) = 32),
    subject      text NOT NULL,
    username     text NOT NULL,
    groups       text[] NOT NULL,
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL
);

CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
