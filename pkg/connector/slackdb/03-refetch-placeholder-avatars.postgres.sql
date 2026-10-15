-- v3 (compatible with v1+): Refetch ghosts that got Slack's placeholder avatar
UPDATE ghost SET metadata=jsonb_set(metadata, '{slack_updated_ts}', '0') WHERE avatar_hash='115dd2254509660adad9d2b0e09b6afbf26846d2e7f57d811f98b6726b7f51af';
