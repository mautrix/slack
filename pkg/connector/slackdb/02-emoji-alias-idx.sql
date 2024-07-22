-- v2 (compatible with v1+): Add index for emoji aliases
CREATE INDEX emoji_alias_idx ON emoji (team_id, alias);
