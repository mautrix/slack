-- v4 (compatible with v1+): Clear emoji URIs cached under Beeper's old direct media prefix
UPDATE emoji SET image_mxc=NULL WHERE image_mxc LIKE 'mxc://local.beeper.com/%\_slack\_%' ESCAPE '\';
