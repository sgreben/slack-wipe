package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"

	"github.com/nlopes/slack"
	"github.com/schollz/progressbar"
)

type teamState struct {
	API          *slack.Client
	RTM          *slack.RTM
	ChannelID    string
	User         string
	UserID       string
	Messages     []slack.Message
	UserMessages []slack.Message
	UserFiles    []slack.File
}

type Team struct {
	Channel string
	Token   string
	teamState
}

var config struct {
	Team
	WipeMessages bool
	WipeFiles    bool
	Quiet        bool
	Path         string `json:"-"`
	AutoApprove  bool
}

var rateLimitTier3 = time.Tick(time.Minute / 50)

func init() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	flag.StringVar(&config.Team.Channel, "channel", "", "channel name (without '#')")
	flag.StringVar(&config.Team.Token, "token", os.Getenv("SLACK_API_TOKEN1"), "API token")
	flag.StringVar(&config.Path, "config", "slack-wipe.json", "")
	flag.BoolVar(&config.WipeMessages, "messages", true, "wipe messages")
	flag.BoolVar(&config.WipeFiles, "files", false, "wipe files")
	flag.BoolVar(&config.AutoApprove, "auto-approve", false, "do not ask for confirmation")
	flag.Parse()

	f, err := os.Open(config.Path)
	if err == nil {
		defer f.Close()
		if err := json.NewDecoder(f).Decode(&config); err != nil {
			log.Fatalf("parse config file %q: %v", config.Path, err)
		}
	}

	if config.Quiet {
		log.SetOutput(ioutil.Discard)
	}
}

func main() {
	team := &config.Team
	team.init()
	if err := team.fetchChannelID(); err != nil {
		log.Fatalf("fetch channel info for channel %q: %v", team.Channel, err)
	}
	if err := team.fetchUserInfo(); err != nil {
		log.Fatalf("fetch user info: %v", err)
	}
	if config.WipeMessages {
		if err := team.fetchMessages(); err != nil {
			log.Fatalf("fetch messages for channel %q: %v", team.Channel, err)
		}
		log.Printf("fetched %d own messages (%d total)", len(team.UserMessages), len(team.Messages))
		if !config.AutoApprove {
			if !approvalPrompt(fmt.Sprintf("wipe all %d messages?", len(team.UserMessages))) {
				log.Fatalf("aborted")
			}
		}
		if err := team.wipeUserMessages(); err != nil {
			log.Fatalf("wipe messages: %v", err)
		}
		log.Print("wiped messages")
	}
	if config.WipeFiles {
		if err := team.fetchFiles(); err != nil {
			log.Fatalf("fetch files for channel %q: %v", team.Channel, err)
		}
		log.Printf("fetched %d own files", len(team.UserFiles))
		if !config.AutoApprove {
			if !approvalPrompt(fmt.Sprintf("wipe all %d files?", len(team.UserFiles))) {
				log.Fatalf("aborted")
			}
		}
		if err := team.wipeUserFiles(); err != nil {
			log.Fatalf("wipe files: %v", err)
		}
		log.Print("wiped files")
	}
}

func approvalPrompt(prompt string) bool {
	r := bufio.NewReader(os.Stdin)
	fmt.Printf(`%s (only the answer "yes" will be accepted): `, prompt)
	answer, err := r.ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "yes"
}

func (t *Team) init() {
	if t.Token == "" {
		log.Fatalf("-token is required")
	}
	t.API = slack.New(t.Token)
	t.RTM = t.API.NewRTM()
	go t.RTM.ManageConnection()
}

func (t *Team) fetchChannelID() error {
	var channels []slack.Channel
	first := true
	cursor := ""
	for first || cursor != "" {
		first = false
		moreChannels, nextCursor, err := t.RTM.GetConversations(&slack.GetConversationsParameters{
			Cursor:          cursor,
			Types:           []string{"private_channel", "public_channel"},
			ExcludeArchived: "false",
		})
		if err != nil {
			return err
		}
		channels = append(channels, moreChannels...)
		cursor = nextCursor
	}
	for _, c := range channels {
		nameMatches := c.Name == t.Channel
		idMatches := c.ID == t.Channel
		if nameMatches || idMatches {
			t.ChannelID = c.ID
			return nil
		}
	}
	return fmt.Errorf("channel not found: %q", t.Channel)
}

func (t *Team) fetchUserInfo() error {
	identity, err := t.RTM.AuthTest()
	if err != nil {
		return err
	}
	t.User = identity.User
	t.UserID = identity.UserID
	return nil
}

func (t *Team) fetchMessages() error {
	first := true
	cursor := ""
	var messages []slack.Message
	for first || cursor != "" {
		first = false
		<-rateLimitTier3
		history, err := t.RTM.GetConversationHistory(&slack.GetConversationHistoryParameters{
			ChannelID: t.ChannelID,
			Cursor:    cursor,
		})
		cursor = history.ResponseMetaData.NextCursor
		if err != nil {
			return err
		}
		messages = append(messages, history.Messages...)
	}
	var userMessages []slack.Message
	for _, m := range messages {
		if m.User == t.UserID {
			userMessages = append(userMessages, m)
		}
	}
	t.Messages = messages
	t.UserMessages = userMessages
	return nil
}

func (t *Team) fetchFiles() error {
	first := true
	var files []slack.File
	params := slack.NewGetFilesParameters()
	params.Page = 1
	params.User = t.UserID
	params.Channel = t.ChannelID
	pageMax := 1
	for first || params.Page <= pageMax {
		first = false
		<-rateLimitTier3
		filesPage, paging, err := t.RTM.GetFiles(params)
		if err != nil {
			return err
		}
		files = append(files, filesPage...)
		if paging != nil {
			pageMax = paging.Pages
		}
		params.Page++
	}
	t.UserFiles = files
	return nil
}

func (t *Team) wipeUserMessages() error {
	var errors []error
	bar := progressbar.New(len(t.UserMessages))
	bar.RenderBlank()
	for _, m := range t.UserMessages {
		bar.Add(1)
		<-rateLimitTier3
		if _, _, err := t.RTM.DeleteMessage(t.ChannelID, m.Timestamp); err != nil {
			errors = append(errors, err)
		}
	}
	bar.Clear()
	if len(errors) > 0 {
		return fmt.Errorf("%d errors (e.g. %v)", len(errors), errors[0])
	}
	return nil
}

func (t *Team) wipeUserFiles() error {
	var errors []error
	bar := progressbar.New(len(t.UserFiles))
	bar.RenderBlank()
	for _, f := range t.UserFiles {
		bar.Add(1)
		<-rateLimitTier3
		if err := t.RTM.DeleteFile(f.ID); err != nil {
			errors = append(errors, err)
		}
	}
	bar.Clear()
	if len(errors) > 0 {
		return fmt.Errorf("%d errors (e.g. %v)", len(errors), errors[0])
	}
	return nil
}
