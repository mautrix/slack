# v25.10

* Switched to calendar versioning.
* Added automatic fetching of channel info when parsing channel mentions, so
  the mention is converted to the channel name instead of showing the raw ID.
* Fixed file name of external images (like gifs) bridged from Slack
  (thanks to [@twouters] in [#70]).
* The deletion of the legacy provisioning API was postponed, but they are
  still deprecated.

[@twouters]: https://github.com/twouters
[#70]: https://github.com/mautrix/slack/pull/70

# v0.2.3 (2025-08-16)

* Deprecated legacy provisioning API. The `/_matrix/provision/v1` endpoints will
  be deleted in the next release.
* Bumped minimum Go version to 1.24.

# v0.2.2 (2025-07-16)

* Updated Docker image to Alpine 3.22.

# v0.2.1 (2025-04-16)

* Fixed auto-linkification in outgoing messages of links whose top-level domain
  contains another shorter top-level domain (e.g. `.dev` which contains `.de`).

# v0.2.0 (2025-03-16)

* Bumped minimum Go version to 1.23.
* Added support for signaling supported features to clients using the
  `com.beeper.room_features` state event.
* Changed mention bridging to never bridge as matrix.to URLs.
* Fixed edits being bridged multiple times if a single chat had multiple
  logged-in Matrix users.

# v0.1.4 (2024-12-16)

* Switched to new API for loading initial chats.
* Updated Docker imager to Alpine 3.21.

# v0.1.3 (2024-11-16)

* Fixed bridged code blocks not being wrapped in a `<code>` element.
* Fixed login command not url-decoding cookies properly.

# v0.1.2 (2024-10-16)

* Fixed bridging newlines in plaintext messages from Matrix to Slack
  (thanks to [@redgoat650] in [#61]).
* Fixed invalid auth not being detected immediately in some cases.

[@redgoat650]: https://github.com/redgoat650
[#61]: https://github.com/mautrix/slack/pull/61

# v0.1.1 (2024-09-16)

* Dropped support for unauthenticated media on Matrix.
* Changed incoming file bridging to roundtrip via disk to avoid storing the
  entire file in memory.
* Fixed sending media messages to Slack threads.

# v0.1.0 (2024-08-16)

Initial release.

Note that when upgrading from an older version, the config file will have to be
recreated. Migrating old configs is not supported. If encryption is used, the
`pickle_key` config option must be set to `maunium.net/go/mautrix-whatsapp` to
be able to read the old database.
