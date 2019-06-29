# slack-wipe

Deletes all your messages and/or files in a single Slack channel.

_NB. code quality = "quick hack". Might clean it up later._

## Usage

```sh
$ slack-wipe -token=API_TOKEN -channel=CHANNEL_NAME -messages=true -files=true
```

```
Usage of slack-wipe:
  -token string
        API token
  -channel string
        channel name (without '#')
  -messages
        wipe messages (default true)
  -files
        wipe files (default false)
  -auto-approve
        do not ask for confirmation (default false)
  -config string
         (default "slack-wipe.json")
```

## API Token

[How to obtain a Slack API token](https://github.com/jackellenberger/emojme#finding-a-slack-token)

## Config schema

```json
{
    "Channel":  "channelname",
    "Token":    "xoxs-..."
}
```
