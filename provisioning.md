# Provisioning API

All endpoints below require the provisioning shared secret in the `Authorization` HTTP header, and the Matrix user ID of the bridge user in the `user_id` query parameter.

## GET `/_matrix/provision/v2/ping`

### Success response format

```json
{
    "mxid": "@user:example.com",
    "management_room": "!foomanagement:example.com",
    "space_room": "!foospace:example.com",
    "admin": false,
    "whitelisted": true,
    "slack_teams": [
        {
            "team": {
                "id": "T0G9PQBBK",
                "name": "Example Team",
                "subdomain": "example",
                "space_mxid": "!fooworkspace:example.com",
                "avatar_url": "mxc://example.com/foovatar"
            },
            "user": {
                "id": "U0G9QF9C6",
                "email": "user@example.com"
            },
            "connected": true,
            "logged_in": true
        }
    ]
}
```

## POST `/_matrix/provision/v2/login`

### Body format

```json
{
    "token": "xoxc-client token",
    "cookie_token": "xoxd-cookie token"
}
```

### Success response format

```json
{
    "team_id": "T0G9PQBBK",
    "team_name": "Example Team",
    "user_id": "U0G9QF9C6",
    "user_email": "user@example.com"
}
```

## POST `/_matrix/provision/v2/logout/{teamID}`

Requires path parameter with the team ID of the Slack team to be logged out (e.g. `T0G9PQBBK`).

Returns 200 with an empty JSON object on successful logout.
