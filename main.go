package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/runes"

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
	Redact       bool
	RedactMarker rune
	IM           string
}

var state struct {
	API          *slack.Client
	RTM          *slack.RTM
	Channel      slack.Channel
	User         string
	UserID       string
	MemberList   []string
	MemberIDMap  map[string]bool
	UserMessages []slack.SearchMessage
	UserFiles    []slack.File
	Users        map[string]slack.User
}

var rateLimitTier4 = time.Tick(time.Minute / 100)
var rateLimitTier3 = time.Tick(time.Minute / 50)
var rateLimitTier2 = time.Tick(time.Minute / 20)

func init() {
	config.RedactMarker = '█'
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime)
	flag.StringVar(&config.Channel, "channel", "", "channel name (without '#')")
	flag.StringVar(&config.IM, "im", "", "comma-separated list of usernames")
	flag.StringVar(&config.Token, "token", "", "API token")
	flag.StringVar(&config.Path, "config", "slack-wipe.json", "")
	flag.BoolVar(&config.WipeMessages, "messages", false, "wipe messages")
	flag.BoolVar(&config.WipeFiles, "files", false, "wipe files")
	flag.BoolVar(&config.AutoApprove, "auto-approve", false, "do not ask for confirmation")
	flag.BoolVar(&config.Redact, "redact", false, "redact messages (instead of delete)")
	flag.Parse()

	f, err := os.Open(config.Path)
	if err == nil {
		defer f.Close()
		if err := json.NewDecoder(f).Decode(&config); err != nil {
			log.Fatalf("parse config file %q: %v", config.Path, err)
		}
	}

	if config.Channel == "" && config.IM == "" {
		log.Fatalf("-channel or -im is required")
	}
	state.MemberList = strings.Split(config.IM, ",")
	if config.Token == "" {
		log.Fatalf("-token is required")
	}
}

func main() {
	state.API = slack.New(config.Token)
	state.RTM = state.API.NewRTM()
	go state.RTM.ManageConnection()
	log.Printf("looking up user for token %s...%s", config.Token[:8], config.Token[len(config.Token)-9:])
	if err := fetchUserInfo(); err != nil {
		log.Fatalf("fetch user info: %v", err)
	}
	log.Printf("user: @%s (@%s)", state.User, state.UserID)
	switch {
	case config.IM != "":
		log.Print("fetching users")
		if err := fetchUsers(); err != nil {
			log.Fatalf("fetch users: %v", err)
		}
		state.MemberIDMap = make(map[string]bool, len(state.MemberList))
		state.MemberIDMap[state.UserID] = true
		for _, m := range state.MemberList {
			m = strings.TrimSpace(m)
			m = strings.TrimPrefix(m, "@")
			state.MemberIDMap[state.Users[m].ID] = true
		}
		log.Printf("looking up channel ID for IM with %v", state.MemberList)
		if err := channelForIM(); err != nil {
			log.Fatalf("fetch channel info for conversation %q: %v", config.IM, err)
		}
	default:
		log.Printf("looking up channel ID for %q", config.Channel)
		if err := channelForChannelName(config.Channel); err != nil {
			log.Fatalf("fetch channel info for channel %q: %v", config.Channel, err)
		}
	}
	log.Printf("channel: %s (%s)", state.Channel.Name, state.Channel.ID)
	if config.WipeMessages {
		fetchAndWipeMessages()
	}
	if config.WipeFiles {
		fetchAndWipeFiles()
	}
}

func fetchAndWipeMessages() {
	verb := "delete"
	if config.Redact {
		verb = "redact"
	}
	switch {
	case state.Channel.IsMpIM || state.Channel.IsIM:
		if err := fetchDirectMessages(); err != nil {
			log.Fatalf("fetch messages for conversation %q: %v", state.Channel.Name, err)
		}
	default:
		if err := fetchMessages(); err != nil {
			log.Fatalf("fetch messages for channel %q: %v", state.Channel.Name, err)
		}
	}
	if !config.AutoApprove {
		if !approvalPrompt(fmt.Sprintf("%s all %d messages?", verb, len(state.UserMessages))) {
			log.Fatalf("aborted")
		}
	}
	if config.Redact {
		if err := redactAllUserMessages(); err != nil {
			log.Fatalf("redact messages: %v", err)
		}
		return
	}
	if err := deleteAllUserMessages(); err != nil {
		log.Fatalf("delete messages: %v", err)
	}
}

func fetchAndWipeFiles() {
	if err := fetchFiles(); err != nil {
		log.Fatalf("fetch files for channel %q: %v", state.Channel.Name, err)
	}
	if !config.AutoApprove {
		if !approvalPrompt(fmt.Sprintf("wipe all %d files?", len(state.UserFiles))) {
			log.Fatalf("aborted")
		}
	}
	if err := deleteAllUserFiles(); err != nil {
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

func channelForIM() error {
	var channels []slack.Channel
	first := true
	cursor := ""
	for first || cursor != "" {
		first = false
		<-rateLimitTier2
		moreChannels, nextCursor, err := state.RTM.GetConversations(&slack.GetConversationsParameters{
			Cursor:          cursor,
			Types:           []string{"mpim", "im"},
			ExcludeArchived: "false",
			Limit:           1000,
		})
		if err != nil {
			return err
		}
		channels = append(channels, moreChannels...)
		cursor = nextCursor
	}
channels:
	for _, c := range channels {
		switch {
		case c.IsIM && len(state.MemberIDMap) == 2 && state.MemberIDMap[c.User]:
			state.Channel = c
			state.Channel.Name = fmt.Sprintf("IM with %v", state.MemberList)
			return nil
		case c.IsMpIM && len(state.MemberIDMap) > 2:
			members, err := usersInConversation(c.ID)
			if err != nil {
				return fmt.Errorf("fetch conversation members: %v", err)
			}
			if len(members) != len(state.MemberIDMap) {
				continue
			}
			for _, m := range members {
				if !state.MemberIDMap[m] {
					continue channels
				}
			}
			state.Channel = c
			return nil
		}
	}
	return fmt.Errorf("conversation not found: %q", config.IM)
}

func usersInConversation(channelID string) ([]string, error) {
	params := &slack.GetUsersInConversationParameters{
		ChannelID: channelID,
	}
	var users []string
	<-rateLimitTier4
	moreUsers, nextCursor, err := state.RTM.GetUsersInConversation(params)
	if err != nil {
		return nil, err
	}
	users = append(users, moreUsers...)
	for nextCursor != "" {
		params.Cursor = nextCursor
		<-rateLimitTier4
		moreUsers, nextCursor, err = state.RTM.GetUsersInConversation(params)
		if err != nil {
			return nil, err
		}
		users = append(users, moreUsers...)
	}
	return users, nil
}

func channelForChannelName(channelName string) error {
	var channels []slack.Channel
	first := true
	cursor := ""
	for first || cursor != "" {
		first = false
		<-rateLimitTier2
		moreChannels, nextCursor, err := state.RTM.GetConversations(&slack.GetConversationsParameters{
			Cursor:          cursor,
			Types:           []string{"private_channel", "public_channel"},
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
		switch {
		case c.Name == channelName:
			state.Channel = c
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

func fetchUsers() error {
	users, err := state.RTM.GetUsers()
	if err != nil {
		return err
	}
	state.Users = make(map[string]slack.User, len(users))
	for _, u := range users {
		state.Users[u.Profile.DisplayName] = u
	}
	return nil
}

func fetchDirectMessages() error {
	params := &slack.GetConversationHistoryParameters{
		ChannelID: state.Channel.ID,
	}
	<-rateLimitTier2
	hist, err := state.RTM.GetConversationHistory(params)
	if err != nil {
		return err
	}
	var userMessages []slack.SearchMessage
	for {
		for _, m := range hist.Messages {
			if m.User == state.UserID {
				userMessages = append(userMessages, slack.SearchMessage{
					Type:        m.Type,
					Channel:     slack.CtxChannel{ID: state.Channel.ID, Name: state.Channel.Name},
					User:        m.User,
					Username:    m.Username,
					Timestamp:   m.Timestamp,
					Text:        m.Text,
					Attachments: m.Attachments,
				})
			}
		}
		nextCursor := hist.ResponseMetaData.NextCursor
		if nextCursor == "" || !hist.HasMore {
			break
		}
		params.Cursor = nextCursor
		<-rateLimitTier2
		hist, err = state.RTM.GetConversationHistory(params)
		if err != nil {
			return err
		}
	}
	state.UserMessages = userMessages
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
	params.Channel = state.Channel.ID
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

func deleteAllUserMessages() error {
	var errors []error
	bar := progressbar.NewOptions(len(state.UserMessages), progressbar.OptionSetDescription("wiping messages"))
	bar.RenderBlank()
	var wg sync.WaitGroup
	wg.Add(len(state.UserMessages))
	for _, m := range state.UserMessages {
		timestamp := m.Timestamp
		go func() {
			defer wg.Done()
			defer bar.Add(1)
			<-rateLimitTier3
			if _, _, err := state.RTM.DeleteMessage(state.Channel.ID, timestamp); err != nil {
				errors = append(errors, err)
			}
		}()
	}
	wg.Wait()
	bar.Finish()
	fmt.Println()
	if len(errors) > 0 {
		return fmt.Errorf("%d errors (e.g. %v)", len(errors), errors[0])
	}
	return nil
}

func deleteAllUserFiles() error {
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

func redactAllUserMessages() error {
	var errors []error
	bar := progressbar.NewOptions(len(state.UserMessages), progressbar.OptionSetDescription("redact messages"))
	bar.RenderBlank()
	var wg sync.WaitGroup
	wg.Add(len(state.UserMessages))
	for _, m := range state.UserMessages {
		timestamp := m.Timestamp
		redacted := redact(m.Text)
		go func() {
			defer wg.Done()
			defer bar.Add(1)
			<-rateLimitTier3
			if _, _, _, err := state.RTM.UpdateMessage(state.Channel.ID, timestamp, redacted); err != nil {
				errors = append(errors, err)
			}
		}()
	}
	wg.Wait()
	bar.Finish()
	fmt.Println()
	if len(errors) > 0 {
		return fmt.Errorf("%d errors (e.g. %v)", len(errors), errors[0])
	}
	return nil
}

var (
	redactTransformer = runes.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return r
		}
		return config.RedactMarker
	})
	redact = redactTransformer.String
)
