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

### Required query parameters

* `user_id`: Matrix ID of user
* `token`: Slack client token (starts with `xoxc-`, stored in the Slack client in local storage: `localConfig_v2/teams/TEAM_ID/token`)
* `cookietoken`: Slack cookie token (starts with `xoxd-`, stored in the Slack client in the `d` cookie)

### Success response format

```
{
    "success": true,
    "teamid": "Slack team ID",
    "userid": "ID of this user on this team"
}
```

## POST `/_matrix/provision/v1/logout`

### Required query parameters

* `user_id`: Matrix ID of user
* `slack_team_id`: ID of this Slack team
