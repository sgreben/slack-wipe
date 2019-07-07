# slack-wipe

Deletes all your messages and/or files in a single Slack channel.

Alternatively, if you specify `-redact` mode, it edits all messages, replacing all non-(space-or-punctuation) characters with `█`:

```text
redact mode test!?
██████ ████ ████!?
```


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
  -redact
        redact messages (instead of delete) (default false)
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
