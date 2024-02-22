-- v16: Add spaces

ALTER TABLE "user" ADD space_room TEXT;
ALTER TABLE team_info ADD space_room TEXT;
ALTER TABLE team_info ADD name_set BOOLEAN DEFAULT false;
ALTER TABLE team_info ADD avatar_set BOOLEAN DEFAULT false;
ALTER TABLE portal ADD in_space BOOLEAN DEFAULT false;
ALTER TABLE user_team ADD in_space BOOLEAN DEFAULT false;
