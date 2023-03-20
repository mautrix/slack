-- v12: Add spaces

ALTER TABLE user ADD space_room TEXT;
ALTER TABLE team_info ADD space_room TEXT;
ALTER TABLE portal ADD in_space BOOLEAN DEFAULT false;
