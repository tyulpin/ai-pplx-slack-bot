package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mmcdole/gofeed"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

var (
	slackClient *slack.Client
	db          *sql.DB
)

func main() {
	// Initialize SQLite database
	var err error
	db, err = sql.Open("sqlite3", "./hyperlinks.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Create table if not exists
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS hyperlinks (id INTEGER PRIMARY KEY AUTOINCREMENT, url TEXT)`)
	if err != nil {
		log.Fatal(err)
	}

	// Initialize Slack client
	slackClient = slack.New(
		os.Getenv("SLACK_BOT_TOKEN"),
		slack.OptionAppLevelToken(os.Getenv("SLACK_APP_TOKEN")),
	)

	socketClient := socketmode.New(
		slackClient,
		socketmode.OptionDebug(true),
		socketmode.OptionLog(log.New(os.Stdout, "socketmode: ", log.Lshortfile|log.LstdFlags)),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func(ctx context.Context, client *socketmode.Client) {
		for {
			select {
			case <-ctx.Done():
				log.Println("Shutting down socketmode listener")
				return
			case event := <-client.Events:
				switch event.Type {
				case socketmode.EventTypeEventsAPI:
					eventsAPIEvent, ok := event.Data.(slack.EventsAPIEvent)
					if !ok {
						continue
					}
					client.Ack(*event.Request)
					switch eventsAPIEvent.Type {
					case slack.EventTypeMessage:
						messageEvent, ok := eventsAPIEvent.InnerEvent.Data.(*slack.MessageEvent)
						if !ok {
							continue
						}
						handleMessage(messageEvent)
					}
				}
			}
		}
	}(ctx, socketClient)

	socketClient.Run()
}

func handleMessage(event *slack.MessageEvent) {
	text := strings.TrimSpace(event.Text)
	if strings.HasPrefix(text, "!perplexity") {
		query := strings.TrimPrefix(text, "!perplexity")
		response := PerplexityAPI(query)
		slackSendMessage(event.Channel, response)
	} else if strings.HasPrefix(text, "!savelink") {
		url := strings.TrimPrefix(text, "!savelink")
		err := saveHyperlink(url)
		if err != nil {
			slackSendMessage(event.Channel, "Error saving hyperlink: "+err.Error())
		} else {
			slackSendMessage(event.Channel, "Hyperlink saved successfully")
		}
	} else if text == "!getlink" {
		url, err := getRandomHyperlink()
		if err != nil {
			slackSendMessage(event.Channel, "Error getting hyperlink: "+err.Error())
		} else if url == "" {
			slackSendMessage(event.Channel, "No hyperlinks available")
		} else {
			slackSendMessage(event.Channel, "Random hyperlink: "+url)
		}
	} else if text == "!listlinks" {
		links, err := getAllHyperlinks()
		if err != nil {
			slackSendMessage(event.Channel, "Error listing hyperlinks: "+err.Error())
		} else if len(links) == 0 {
			slackSendMessage(event.Channel, "No hyperlinks saved")
		} else {
			message := "Saved hyperlinks:\n" + strings.Join(links, "\n")
			slackSendMessage(event.Channel, message)
		}
	} else if text == "!summarize" {
		url, err := getRandomHyperlink()
		if err != nil {
			slackSendMessage(event.Channel, "Error getting hyperlink: "+err.Error())
		} else if url == "" {
			slackSendMessage(event.Channel, "No hyperlinks available")
		} else {
			article, err := fetchRSS(url)
			if err != nil {
				slackSendMessage(event.Channel, "Error fetching article: "+err.Error())
			} else {
				summary := PerplexityAPI("Summarize this article: " + article)
				slackSendMessage(event.Channel, "Summary of article from "+url+":\n\n"+summary)
			}
		}
	}
}

func PerplexityAPI(query string) string {
	apiKey := os.Getenv("PERPLEXITY_API_KEY")
	url := "https://api.perplexity.ai/chat/completions"

	requestBody, _ := json.Marshal(map[string]interface{}{
		"model": "mixtral-8x7b-instruct",
		"messages": []map[string]string{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": query},
		},
	})

	req, _ := http.NewRequest("POST", url, strings.NewReader(string(requestBody)))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "Error: " + err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := message["content"].(string); ok {
					return content
				}
			}
		}
	}

	return "No response from Perplexity API"
}

func slackSendMessage(channel, message string) {
	_, _, err := slackClient.PostMessage(channel, slack.MsgOptionText(message, false))
	if err != nil {
		log.Printf("Error sending message: %v", err)
	}
}

func saveHyperlink(url string) error {
	_, err := db.Exec("INSERT INTO hyperlinks (url) VALUES (?)", url)
	return err
}

func getRandomHyperlink() (string, error) {
	var url string
	err := db.QueryRow("SELECT url FROM hyperlinks ORDER BY RANDOM() LIMIT 1").Scan(&url)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_, err = db.Exec("DELETE FROM hyperlinks WHERE url = ?", url)
	if err != nil {
		return "", err
	}
	return url, nil
}

func getAllHyperlinks() ([]string, error) {
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

func fetchRSS(url string) (string, error) {
	fp := gofeed.NewParser()
	feed, err := fp.ParseURL(url)
	if err != nil {
		return "", err
	}

	if len(feed.Items) > 0 {
		item := feed.Items[0]
		return fmt.Sprintf("Title: %s\n\nDescription: %s", item.Title, item.Description), nil
	}

	return "", fmt.Errorf("no items found in the feed")
}
