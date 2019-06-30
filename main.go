package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/nlopes/slack"
	"github.com/schollz/progressbar"
)

var config struct {
	Channel      string
	Token        string
	WipeMessages bool
	WipeFiles    bool
	Path         string `json:"-"`
	AutoApprove  bool
}

var state struct {
	API          *slack.Client
	RTM          *slack.RTM
	ChannelID    string
	User         string
	UserID       string
	UserMessages []slack.SearchMessage
	UserFiles    []slack.File
}

var rateLimitTier3 = time.Tick(time.Minute / 50)
var rateLimitTier2 = time.Tick(time.Minute / 20)

func init() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime)
	flag.StringVar(&config.Channel, "channel", "", "channel name (without '#')")
	flag.StringVar(&config.Token, "token", "", "API token")
	flag.StringVar(&config.Path, "config", "slack-wipe.json", "")
	flag.BoolVar(&config.WipeMessages, "messages", false, "wipe messages")
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

	if config.Channel == "" {
		log.Fatalf("-channel is required")
	}
	if config.Token == "" {
		log.Fatalf("-token is required")
	}
}

func main() {
	state.API = slack.New(config.Token)
	state.RTM = state.API.NewRTM()
	go state.RTM.ManageConnection()
	log.Printf("looking up channel ID for %q", config.Channel)
	if err := lookUpChannelID(config.Channel); err != nil {
		log.Fatalf("fetch channel info for channel %q: %v", config.Channel, err)
	}
	log.Printf("channel ID: %s", state.ChannelID)
	log.Printf("looking up user for token %s...%s", config.Token[:8], config.Token[len(config.Token)-9:])
	if err := fetchUserInfo(); err != nil {
		log.Fatalf("fetch user info: %v", err)
	}
	log.Printf("user: @%s (@%s)", state.User, state.UserID)
	if config.WipeMessages {
		fetchAndWipeMessages()
	}
	if config.WipeFiles {
		fetchAndWipeFiles()
	}
}

func fetchAndWipeMessages() {
	if err := fetchMessages(); err != nil {
		log.Fatalf("fetch messages for channel %q: %v", config.Channel, err)
	}
	if !config.AutoApprove {
		if !approvalPrompt(fmt.Sprintf("wipe all %d messages?", len(state.UserMessages))) {
			log.Fatalf("aborted")
		}
	}
	if err := wipeUserMessages(); err != nil {
		log.Fatalf("wipe messages: %v", err)
	}
}

func fetchAndWipeFiles() {
	if err := fetchFiles(); err != nil {
		log.Fatalf("fetch files for channel %q: %v", config.Channel, err)
	}
	if !config.AutoApprove {
		if !approvalPrompt(fmt.Sprintf("wipe all %d files?", len(state.UserFiles))) {
			log.Fatalf("aborted")
		}
	}
	if err := wipeUserFiles(); err != nil {
		log.Fatalf("wipe files: %v", err)
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

func lookUpChannelID(channelName string) error {
	var channels []slack.Channel
	first := true
	cursor := ""
	for first || cursor != "" {
		first = false
		<-rateLimitTier2
		moreChannels, nextCursor, err := state.RTM.GetConversations(&slack.GetConversationsParameters{
			Cursor:          cursor,
			Types:           []string{"private_channel", "public_channel", "mpim", "im"},
			ExcludeArchived: "false",
			Limit:           1000,
		})
		if err != nil {
			return err
		}
		channels = append(channels, moreChannels...)
		cursor = nextCursor
	}
	for _, c := range channels {
		if c.Name == channelName {
			state.ChannelID = c.ID
			return nil
		}
	}
	return fmt.Errorf("channel not found: %q", channelName)
}

func fetchUserInfo() error {
	<-rateLimitTier3
	identity, err := state.RTM.AuthTest()
	if err != nil {
		return err
	}
	state.User = identity.User
	state.UserID = identity.UserID
	return nil
}

func fetchMessages() error {
	params := slack.NewSearchParameters()
	params.Count = 100
	query := fmt.Sprintf("in:#%s from:@%s", config.Channel, state.UserID)
	<-rateLimitTier2
	resp, err := state.RTM.SearchMessages(query, params)
	if err != nil {
		return err
	}
	messages := resp.Matches
	pageMax := resp.PageCount
	params.Page++
	bar := progressbar.NewOptions(pageMax, progressbar.OptionSetDescription("fetching messages"))
	bar.Add(1)
	for params.Page <= pageMax {
		<-rateLimitTier2
		resp, err := state.RTM.SearchMessages(query, params)
		if err != nil {
			return err
		}
		messages = append(messages, resp.Matches...)
		pageMax = resp.PageCount
		params.Page++
		bar.Add(1)
	}
	bar.Finish()
	fmt.Println()
	var userMessages []slack.SearchMessage
	for _, m := range messages {
		if m.User == state.UserID {
			userMessages = append(userMessages, m)
		}
	}
	state.UserMessages = userMessages
	return nil
}

func fetchFiles() error {
	params := slack.NewGetFilesParameters()
	params.Count = 200
	params.User = state.UserID
	params.Channel = state.ChannelID
	<-rateLimitTier3
	files, paging, err := state.RTM.GetFiles(params)
	if err != nil {
		return err
	}
	pageMax := 1
	if paging != nil {
		pageMax = paging.Pages
	}
	params.Page++
	bar := progressbar.NewOptions(pageMax, progressbar.OptionSetDescription("fetching files"))
	bar.Add(1)
	for params.Page <= pageMax {
		<-rateLimitTier3
		filesPage, paging, err := state.RTM.GetFiles(params)
		if err != nil {
			return err
		}
		files = append(files, filesPage...)
		if paging != nil {
			pageMax = paging.Pages
		}
		params.Page++
		bar.Add(1)
	}
	bar.Finish()
	fmt.Println()
	state.UserFiles = files
	return nil
}

func wipeUserMessages() error {
	var errors []error
	bar := progressbar.NewOptions(len(state.UserMessages), progressbar.OptionSetDescription("wiping messages"))
	bar.RenderBlank()
	for _, m := range state.UserMessages {
		bar.Add(1)
		<-rateLimitTier3
		if _, _, err := state.RTM.DeleteMessage(state.ChannelID, m.Timestamp); err != nil {
			errors = append(errors, err)
		}
	}
	bar.Finish()
	fmt.Println()
	if len(errors) > 0 {
		return fmt.Errorf("%d errors (e.g. %v)", len(errors), errors[0])
	}
	return nil
}

func wipeUserFiles() error {
	var errors []error
	bar := progressbar.NewOptions(len(state.UserFiles), progressbar.OptionSetDescription("wiping files"))
	bar.RenderBlank()
	for _, f := range state.UserFiles {
		bar.Add(1)
		<-rateLimitTier3
		if err := state.RTM.DeleteFile(f.ID); err != nil {
			errors = append(errors, err)
		}
	}
	bar.Finish()
	fmt.Println()
	if len(errors) > 0 {
		return fmt.Errorf("%d errors (e.g. %v)", len(errors), errors[0])
	}
	return nil
}
