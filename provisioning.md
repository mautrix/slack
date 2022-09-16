# Provisioning API

All endpoints below require the provisioning shared secret in the `Authorization` HTTP header, and the Matrix user ID of the bridge user in the `user_id` query parameter.

## GET `/_matrix/provision/v1/ping`

### Success response format

```
{
    "management_room": "Management room ID for this user",
    "mxid": "Matrix ID of this user",
    "slack": {
        "logged_in_teams": {
            "SLACK_TEAM_ID": {
                "user_id": "Slack user ID of this user on this team",
                "user_email": "Email address of this user on this team",
                "team_name": "Name of this Slack team"
            }
        }
    }
}
```

## POST `/_matrix/provision/v1/login`

### Body format

```
{
    "token": "xoxc-client token",
    "cookietoken": "xoxd-cookie token"
}
```

### Success response format

```
{
    "success": true,
    "teamid": "Slack team ID",
    "userid": "ID of this user on this team"
}
```

## POST `/_matrix/provision/v1/logout`

### Body format

```
{
    "slack_team_id": "Slack team ID"
}
```

Returns 200 on successful logout.
