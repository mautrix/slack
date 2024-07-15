# Features & roadmap

* Matrix → Slack
    * [x] Message content
        * [x] Plain text
        * [x] Formatted text
        * [x] User pings
        * [x] Media and files
        * [x] Edits
        * [x] Threads
        * [x] Replies (as Slack threads)
    * [x] Reactions
    * [x] Typing status
    * [x] Message redaction
    * [x] Mark room as read
* Slack → Matrix
    * [ ] Message content
        * [x] Plain text
        * [x] Formatted text
        * [x] User pings
        * [x] Media and files
        * [x] Edits
        * [x] Threads (as Matrix native threads with fallback Matrix reply)
        * [x] Custom Slack emoji
    * [x] Reactions
    * [x] Typing status
    * [x] Message deletion
    * [x] Reading pre-login message history
    * [x] Conversation types
        * [x] Channel (including Slack Connect)
        * [x] Group DM
        * [x] 1:1 DM
    * [x] Initial conversation metadata
        * [x] Name
        * [x] Topic
        * [x] Description
        * [x] Channel members
    * [ ] Conversation metadata changes
        * [ ] Name
        * [x] Topic
        * [x] Description
    * [x] Mark conversation as read
* Misc
    * [x] Automatic portal creation
        * [x] On login (with token, not with password)
        * [x] When receiving message
        * [ ] When added to conversation
    * [ ] Creating DM by inviting user to Matrix room
    * [x] Using your own Matrix account for messages sent from your Slack client
    * [x] Shared channel portals between different Matrix users
    * [ ] Using relay bot to bridge to Slack
