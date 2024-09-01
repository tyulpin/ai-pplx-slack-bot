package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	//"io/ioutil"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mmcdole/gofeed"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

var (
	slackClient     *slack.Client
	socketClient    *socketmode.Client
	db              *sql.DB
	perplexityAPI   string
	lastUserInput   string
	lastBotResponse string
)

func main() {
	// Initialize Slack client
	slackToken := os.Getenv("SLACK_BOT_TOKEN")
	appToken := os.Getenv("SLACK_APP_TOKEN")
	perplexityAPI = os.Getenv("PERPLEXITY_API_KEY")

	slackClient = slack.New(slackToken, slack.OptionAppLevelToken(appToken))
	socketClient = socketmode.New(
		slackClient,
		socketmode.OptionDebug(true),
		socketmode.OptionLog(log.New(os.Stdout, "socketmode: ", log.Lshortfile|log.LstdFlags)),
	)

	// Initialize SQLite database
	var err error
	db, err = sql.Open("sqlite3", "./hyperlinks.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Create hyperlinks table if not exists
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS hyperlinks (id INTEGER PRIMARY KEY AUTOINCREMENT, url TEXT)`)
	if err != nil {
		log.Fatal(err)
	}

	// Start listening to events
	go func() {
		for evt := range socketClient.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					continue
				}
				socketClient.Ack(*evt.Request)
				handleEventAPI(eventsAPIEvent)
			}
		}
	}()

	err = socketClient.Run()
	if err != nil {
		log.Fatal(err)
	}
}

func handleEventAPI(event slackevents.EventsAPIEvent) {
	switch ev := event.InnerEvent.Data.(type) {
	case *slack.MessageEvent:
		if ev.BotID == "" {
			handleUserMessage(ev)
		}
	}
}

func handleUserMessage(ev *slack.MessageEvent) {
	text := strings.TrimSpace(ev.Text)
	if strings.HasPrefix(text, "!save ") {
		url := strings.TrimPrefix(text, "!save ")
		err := saveHyperlink(url)
		if err != nil {
			slackSendMessage(ev.Channel, fmt.Sprintf("Error saving hyperlink: %v", err))
		} else {
			slackSendMessage(ev.Channel, "Hyperlink saved successfully!")
		}
	} else if text == "!list" {
		links, err := listHyperlinks()
		if err != nil {
			slackSendMessage(ev.Channel, fmt.Sprintf("Error listing hyperlinks: %v", err))
		} else {
			slackSendMessage(ev.Channel, fmt.Sprintf("Saved hyperlinks:\n%s", strings.Join(links, "\n")))
		}
	} else if text == "!random" {
		link, err := getRandomHyperlink()
		if err != nil {
			slackSendMessage(ev.Channel, fmt.Sprintf("Error getting random hyperlink: %v", err))
		} else {
			slackSendMessage(ev.Channel, fmt.Sprintf("Random hyperlink: %s", link))
		}
	} else if text == "!summarize" {
		link, err := getRandomHyperlink()
		if err != nil {
			slackSendMessage(ev.Channel, fmt.Sprintf("Error getting random hyperlink: %v", err))
		} else {
			summary, err := summarizeArticle(link)
			if err != nil {
				slackSendMessage(ev.Channel, fmt.Sprintf("Error summarizing article: %v", err))
			} else {
				slackSendMessage(ev.Channel, fmt.Sprintf("Summary of %s:\n%s", link, summary))
			}
		}
	} else {
		if text != lastUserInput {
			lastUserInput = text
			response, err := PerplexityAPI(text)
			if err != nil {
				slackSendMessage(ev.Channel, fmt.Sprintf("Error: %v", err))
			} else {
				lastBotResponse = response
				slackSendMessage(ev.Channel, response)
			}
		} else {
			slackSendMessage(ev.Channel, lastBotResponse)
		}
	}
}

func PerplexityAPI(input string) (string, error) {
	url := "https://api.perplexity.ai/chat/completions"
	payload := map[string]interface{}{
		"model": "gpt-3.5-turbo",
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": input,
			},
		},
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonPayload)))
	if err != nil {
		return "", err
	}

	req.Header.Add("Authorization", "Bearer "+perplexityAPI)
	req.Header.Add("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return "", err
	}

	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return "", fmt.Errorf("invalid response format")
	}

	message, ok := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid response format")
	}

	content, ok := message["content"].(string)
	if !ok {
		return "", fmt.Errorf("invalid response format")
	}

	return content, nil
}

func slackSendMessage(channelID, message string) {
	_, _, err := slackClient.PostMessage(channelID, slack.MsgOptionText(message, false))
	if err != nil {
		log.Printf("Error sending message: %v", err)
	}
}

func saveHyperlink(url string) error {
	_, err := db.Exec("INSERT INTO hyperlinks (url) VALUES (?)", url)
	return err
}

func listHyperlinks() ([]string, error) {
	rows, err := db.Query("SELECT url FROM hyperlinks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []string
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			return nil, err
		}
		links = append(links, url)
	}
	return links, nil
}

func getRandomHyperlink() (string, error) {
	var url string
	var id int
	err := db.QueryRow("SELECT id, url FROM hyperlinks ORDER BY RANDOM() LIMIT 1").Scan(&id, &url)
	if err != nil {
		return "", err
	}

	_, err = db.Exec("DELETE FROM hyperlinks WHERE id = ?", id)
	if err != nil {
		return "", err
	}

	return url, nil
}

func summarizeArticle(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fp := gofeed.NewParser()
	feed, err := fp.ParseURLWithContext(url, ctx)
	if err != nil {
		return "", err
	}

	if len(feed.Items) == 0 {
		return "", fmt.Errorf("no items found in the feed")
	}

	article := feed.Items[0]
	summary := fmt.Sprintf("Title: %s\n\nDescription: %s", article.Title, article.Description)

	return summary, nil
}
