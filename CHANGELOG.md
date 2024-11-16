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
