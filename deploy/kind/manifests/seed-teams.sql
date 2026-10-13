-- Demo group -> team mapping for the kind environment. Phase 3 replaces this
-- with the admin UI. Idempotent.
INSERT INTO teams (name, display_name) VALUES
    ('payments', 'Payments'),
    ('catalog', 'Catalog')
ON CONFLICT (name) DO NOTHING;

INSERT INTO team_groups (team_name, group_name) VALUES
    ('payments', 'team-payments'),
    ('catalog', 'team-catalog')
ON CONFLICT DO NOTHING;
