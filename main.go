package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mmcdole/gofeed"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

var (
	slackToken    = "SLACK_BOT_TOKEN"
	appToken      = "SLACK_APP_TOKEN"
	perplexityAPI = "https://api.perplexity.ai/your-endpoint"
	dbFile        = "hyperlinks.db"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type PerplexityRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type PerplexityResponse struct {
	Content string `json:"content"`
}

func main() {
	ctx := context.Background()
	api := slack.New(slackToken, slack.OptionAppLevelToken(appToken))
	client := socketmode.New(api)
	db := initDB()

	go func() {
		for evt := range client.Events {
			switch evt.Type {
			case socketmode.EventTypeInteractive:
				callback := evt.Data.(slack.InteractionCallback)
				handleSlackEvent(ctx, client, db, callback)
			}
		}
	}()

	client.Run()
}

func handleSlackEvent(ctx context.Context, client *socketmode.Client, db *sql.DB, callback slack.InteractionCallback) {
	api := client.Client

	// Process message events
	if callback.Type == slack.InteractionTypeMessage {
		messageText := callback.Message.Text
		channelID := callback.Channel.ID

		// Check for specific commands
		switch {
		case messageText == "!listlinks":
			// List all saved hyperlinks
			urls, err := listAllLinks(db)
			if err != nil {
				slackSendMessage(api, channelID, fmt.Sprintf("Error listing links: %v", err))
				return
			}
			response := "Saved Hyperlinks:\n" + strings.Join(urls, "\n")
			slackSendMessage(api, channelID, response)

		case messageText == "!randomlink":
			// Fetch a random hyperlink
			url, err := fetchRandomLink(db)
			if err != nil {
				slackSendMessage(api, channelID, fmt.Sprintf("Error fetching random link: %v", err))
				return
			}
			slackSendMessage(api, channelID, fmt.Sprintf("Random Link: %s", url))

		case strings.HasPrefix(messageText, "!save "):
			// Save a new hyperlink
			url := strings.TrimPrefix(messageText, "!save ")
			err := storage(db, url)
			if err != nil {
				slackSendMessage(api, channelID, fmt.Sprintf("Error saving link: %v", err))
				return
			}
			slackSendMessage(api, channelID, fmt.Sprintf("Link saved: %s", url))

		case strings.HasPrefix(messageText, "!rss "):
			// Fetch and summarize RSS feed
			url := strings.TrimPrefix(messageText, "!rss ")
			summary, err := fetchRSSSummary(url)
			if err != nil {
				slackSendMessage(api, channelID, fmt.Sprintf("Error fetching RSS feed: %v", err))
				return
			}
			slackSendMessage(api, channelID, fmt.Sprintf("RSS Summary:\n%s", summary))

		default:
			// Handle other messages, send to Perplexity API
			request := map[string]interface{}{
				"model": "default",
				"messages": []Message{
					{Role: "user", Content: messageText},
				},
			}
			response, err := PerplexityAPI(ctx, request)
			if err != nil {
				slackSendMessage(api, channelID, fmt.Sprintf("Error communicating with Perplexity API: %v", err))
				return
			}
			slackSendMessage(api, channelID, response)
		}
	}
}

func PerplexityAPI(ctx context.Context, request map[string]interface{}) (string, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", perplexityAPI, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var perplexityResp PerplexityResponse
	if err := json.NewDecoder(resp.Body).Decode(&perplexityResp); err != nil {
		return "", err
	}

	return perplexityResp.Content, nil
}

func slackSendMessage(api *slack.Client, channelID, message string) error {
	_, _, err := api.PostMessage(channelID, slack.MsgOptionText(message, false))
	return err
}

func initDB() *sql.DB {
	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	createTableSQL := `CREATE TABLE IF NOT EXISTS hyperlinks (
		"id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"url" TEXT NOT NULL
	);`
	_, err = db.Exec(createTableSQL)
	if err != nil {
		log.Fatalf("failed to create table: %v", err)
	}

	return db
}

func storage(db *sql.DB, url string) error {
	_, err := db.Exec("INSERT INTO hyperlinks (url) VALUES (?)", url)
	return err
}

func fetchRandomLink(db *sql.DB) (string, error) {
	row := db.QueryRow("SELECT id, url FROM hyperlinks ORDER BY RANDOM() LIMIT 1")
	var id int
	var url string
	if err := row.Scan(&id, &url); err != nil {
		return "", err
	}

	_, err := db.Exec("DELETE FROM hyperlinks WHERE id = ?", id)
	if err != nil {
		return "", err
	}

	return url, nil
}

func listAllLinks(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT url FROM hyperlinks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var urls []string
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			return nil, err
		}
		urls = append(urls, url)
	}

	return urls, nil
}

func fetchRSSSummary(url string) (string, error) {
	fp := gofeed.NewParser()
	feed, err := fp.ParseURL(url)
	if err != nil {
		return "", err
	}

	var summary string
	for _, item := range feed.Items {
		summary += fmt.Sprintf("Title: %s\nLink: %s\n", item.Title, item.Link)
	}

	return summary, nil
}
