# slack-wipe

Deletes all your messages and/or files in a single Slack channel.

## Usage

```sh
$ slack-wipe -token=API_TOKEN -channel=CHANNEL_NAME -messages -files
```

```
Usage of slack-wipe:
  -token string
        API token
  -channel string
        channel name (without '#')
  -messages
        wipe messages (default false)
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

Put this in a `slack-wipe.json` to avoid having to type out token/channel info on the command line:

```json
{
    "Channel":  "channelname",
    "Token":    "xoxs-..."
}
```
