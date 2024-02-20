package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

type PerplexityRequest struct {
	Model    string     `json:"model"`
	Messages []messages `json:"messages"`
}

type PerplexityResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

type messages struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Choice struct {
	Index   int      `json:"index"`
	Message messages `json:"message"`
}

// PerplexityAPIURL is the URL of the Perplexity API.
const PerplexityAPIURL = "https://api.perplexity.ai/chat/completions"

func PerplexityAPI(request PerplexityRequest) (string, error) {
	// Replace with your Perplexity API URL
	url := "https://api.perplexity.ai/chat/completions"

	PPLX_API_KEY := os.Getenv("PPLX_API_KEY")
	if PPLX_API_KEY == "" {
		fmt.Fprintf(os.Stderr, "PERPLEXITY_API_KEY must be set.\n")
		os.Exit(1)
	}

	//Convert the body to JSON
	jsonBody, err := json.Marshal(request)
	if err != nil {
		return "messages{}", err
	}

	// Create a request
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return "messages{}", err
	}

	// Set the request header
	req.Header.Set("accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+PPLX_API_KEY)

	// Create a client
	client := &http.Client{}

	// Send the request
	resp, err := client.Do(req)
	if err != nil {
		return "messages{}", err
	}
	defer resp.Body.Close()

	//Read the response body
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "messages{}", err
	}

	return string(responseBody), nil
}

// SlackSend sends a message to Slack.
func SlackSend(text string) error {

	botToken := os.Getenv("SLACK_BOT_TOKEN")
	if botToken == "" {
		fmt.Fprintf(os.Stderr, "SLACK_BOT_TOKEN must be set.\n")
		os.Exit(1)
	}
	// Create the Slack client.
	api := slack.New(botToken)

	// channelID := os.Getenv("SLACK_CHANNEL_ID")

	// Send the message to Slack.
	_, _, err := api.PostMessage("general", slack.MsgOptionText(text, false))
	if err != nil {
		return err
	}

	return nil
}

func main() {

	err := godotenv.Load(".env")
	if err != nil {
		log.Println("not able to load env")
		return
	}

	botToken := os.Getenv("SLACK_BOT_TOKEN")
	if botToken == "" {
		fmt.Fprintf(os.Stderr, "SLACK_BOT_TOKEN must be set.\n")
		os.Exit(1)
	}

	appToken := os.Getenv("SLACK_APP_TOKEN")
	if appToken == "" {
		fmt.Fprintf(os.Stderr, "SLACK_APP_TOKEN must be set.\n")
		os.Exit(1)
	}

	if !strings.HasPrefix(appToken, "xapp-") {
		fmt.Fprintf(os.Stderr, "SLACK_APP_TOKEN must have the prefix \"xapp-\".")
	}

	// Create the Slack socket mode client.
	api := slack.New(
		botToken,
		slack.OptionDebug(true),
		slack.OptionLog(log.New(os.Stdout, "api: ", log.Lshortfile|log.LstdFlags)),
		slack.OptionAppLevelToken(appToken),
	)

	client := socketmode.New(
		api,
		socketmode.OptionDebug(true),
		socketmode.OptionLog(log.New(os.Stdout, "socketmode: ", log.Lshortfile|log.LstdFlags)),
	)

	// Listen for events and respond to them.
	go func() {

		lastMessage := make(map[string]string)

		for evt := range client.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				// Parse the event.
				eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					fmt.Printf("unexpected type for EventsAPI event: %T\n", eventsAPIEvent)
					continue
				}

				// Handle the event.
				if eventsAPIEvent.Type == slackevents.CallbackEvent {
					innerEvent := eventsAPIEvent.InnerEvent
					switch ev := innerEvent.Data.(type) {
					case *slackevents.MessageEvent:
						// Ignore bot messages.
						if ev.BotID != "" {
							continue
						}

						// Send the message to the Perplexity API.

						PerplexityRequest := PerplexityRequest{
							Model: "mistral-7b-instruct",
							Messages: []messages{
								{Role: "system",
									Content: "Be precise and concise."},
								{Role: "user",
									Content: ev.Text},
							},
						}

						response, err := PerplexityAPI(PerplexityRequest)
						if err != nil {
							log.Printf("Error calling Perplexity API: %v", err)
							//SlackSend(response)
							continue
						}

						var perplexityResponse PerplexityResponse
						err = json.Unmarshal([]byte(response), &perplexityResponse)
						if err != nil {
							log.Printf("Error decoding Perplexity API response: %v", err)
							continue
						}

						if ev.Text != lastMessage[ev.User] {

							if len(perplexityResponse.Choices) > 0 {

								SlackSend(perplexityResponse.Choices[0].Message.Content)
								lastMessage[ev.User] = ev.Text
							}
						}
					}
				}
			}
		}
	}()

	// Connect the socket mode client.
	client.Run()
}
