# Provisioning API

All endpoints below require the provisioning shared secret in the `Authorization` HTTP header, and the Matrix user ID of the bridge user in the `user_id` query parameter.

## GET `/_matrix/provision/v1/ping`

### Success response format

```
{
    "management_room": "Management room ID for this user",
    "mxid": "Matrix ID of this user",
    "puppets": [
        {
            "puppetId": "TEAMID-USERID",
            "puppetMxid": "Matrix ID of user",
            "data": {
                "team": {
                    "id": "TEAMID",
                    "name": "Name of Slack team"
                },
                "self": {
                    "id": "USERID",
                    "name": "Name of Slack user"
                }
            },
            "userId": "USERID"
        }
    ]
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
