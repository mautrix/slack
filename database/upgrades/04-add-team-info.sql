-- v4: add team info table

CREATE TABLE "team_info" (
    team_id TEXT NOT NULL UNIQUE,
    team_domain TEXT,
    team_url TEXT,
    team_name TEXT,
    avatar TEXT,
    avatar_url TEXT
);
