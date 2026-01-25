package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/slack-go/slack"
	"google.golang.org/genai"
)

type UniqueMention struct {
	Timestamp string
	ChannelId string
}

type ConversationsResponse struct {
	// I want immutability
	// The struct is not big enough to make a pointer
	ConversationContext []ConversationResponseEntry
}

type ThreadMessage struct {
	Text      string
	Timestamp string
}
type ConversationResponseEntry struct {
	MentionPermalink string
	MentionText      string
	MentionChannelId string
	MentionTimestamp string
	Messages         []ThreadMessage
}

type GenAiResponse struct {
	Summary        []string
	Actionable     string
	ActionRequired []string
	Priority       string
}

func cleanJSON(input string) string {
	input = strings.TrimSpace(input)

	// Remove ```json and ``` if present
	input = strings.TrimPrefix(input, "```json")
	input = strings.TrimPrefix(input, "```")
	input = strings.TrimSuffix(input, "```")

	return strings.TrimSpace(input)
}

func filterMentions(allMentions *slack.SearchMessages, userId string) ([]slack.SearchMessage, error) {
	// msg.Type should be 'message'
	// Not taking the devrev tickets
	// use the blocks -> rich_text -> rich_text_section -> type user to find for legitimate mention
	// msg.Channel.is_private should be false
	var filteredMentions []slack.SearchMessage

	threadsTaken := make(map[UniqueMention]struct{})

	for _, msg := range allMentions.Matches {
		if msg.Type != "message" ||
			msg.Channel.IsPrivate ||
			msg.Username == "devrev" {
			continue
		}

		for _, blk := range msg.Blocks.BlockSet {
			if blk.BlockType() != slack.MBTRichText {
				continue
			}

			richTextBlock, ok := blk.(*slack.RichTextBlock)

			if !ok {
				continue
			}

			for _, rtElem := range richTextBlock.Elements {
				if rtElem.RichTextElementType() != slack.RTESection {
					continue
				}
				richTextSection, ok2 := rtElem.(*slack.RichTextSection)

				if !ok2 {
					continue
				}

				for _, richTextSectionElem := range richTextSection.Elements {
					if richTextSectionElem.RichTextSectionElementType() != slack.RTSEUser {
						continue
					}

					//check the mentioned user
					richTextSectionUser, ok3 := richTextSectionElem.(*slack.RichTextSectionUserElement)

					if !ok3 {
						continue
					}

					if richTextSectionUser.UserID == userId {
						// this makes the current msg valid candidate for mention
						// now this should be only message we take for this thread
						uniqueKey := UniqueMention{
							Timestamp: msg.Timestamp,
							ChannelId: msg.Channel.ID,
						}
						if _, exists := threadsTaken[uniqueKey]; !exists {
							threadsTaken[uniqueKey] = struct{}{}
							filteredMentions = append(filteredMentions, msg)
							break
						}

					}
				}
				break
			}
			break
		}
	}

	return filteredMentions, nil
}

func getMentions(slackClient *slack.Client, userId string) ([]slack.SearchMessage, error) {
	// prepare the query to search for messages mentioning the user in the last day
	yesterday := time.Now().AddDate(0, 0, -4).Format("2006-01-02")
	today := time.Now().Format("2006-01-02")
	query := fmt.Sprintf("<@%s> after:%s before:%s", userId, yesterday, today)

	params := slack.SearchParameters{
		Sort:          "timestamp",
		SortDirection: "desc",
		Count:         40, // taking at max 40 mentions in a day
	}
	// do the search api call
	res, err := slackClient.SearchMessages(query, params)

	if err != nil {
		return nil, err
	}

	//fmt.Println("total matches:", res.Total)
	//fmt.Println("total on the page1:", len(res.Matches))

	//total_mentions := res.Total
	//total_mentions_first_page := len(res.Matches)

	filteredMentions, err := filterMentions(res, userId)

	//accuracy := float64(len(filteredMentions)) / float64(res.Total) * 100.0

	return filteredMentions, nil
}

func getConversation(SlackClient *slack.Client, filteredMentions []slack.SearchMessage) (*ConversationsResponse, error) {

	conversationsResponse := &ConversationsResponse{}
	// Rule: # of mentions = # of conversations

	// we will iterate through each mention in the mentions array
	for _, mention := range filteredMentions {
		// for each we have the channelId and threadTs
		channelId := mention.Channel.ID
		threadTs := mention.Timestamp

		parsedUrl, urlParseError := url.Parse(mention.Permalink)

		if urlParseError != nil {
			return nil, urlParseError
		}
		parentThreadTs := parsedUrl.Query().Get("thread_ts")

		if len(parentThreadTs) == 0 {
			parentThreadTs = threadTs
		}

		// using these values we will get the entire thread conversation
		params := &slack.GetConversationRepliesParameters{
			Limit:     200,
			ChannelID: channelId,
			// when querying for thread replies, we need to add the parent thread
			// timestamp in the Timestamp field
			Timestamp: parentThreadTs,
		}

		// sorted in increasing order of timestamp
		// threadConversations is a slice of Message
		threadConversations, _, _, getConversationRepliesError := SlackClient.GetConversationReplies(params)

		if getConversationRepliesError != nil {
			return nil, getConversationRepliesError
		}

		// to generate the response I need to make a ConversationResponseEntry
		var conversationEntry ConversationResponseEntry

		conversationEntry.MentionPermalink = mention.Permalink
		conversationEntry.MentionText = mention.Text
		conversationEntry.MentionChannelId = channelId
		conversationEntry.MentionTimestamp = threadTs

		for _, threadConversation := range threadConversations {
			threadConversationText := threadConversation.Msg.Text
			threadConversationTimestamp := threadConversation.Msg.Timestamp

			threadConversationTextStruct := ThreadMessage{
				Text:      threadConversationText,
				Timestamp: threadConversationTimestamp,
			}
			conversationEntry.Messages = append(conversationEntry.Messages, threadConversationTextStruct)
		}

		conversationsResponse.ConversationContext = append(conversationsResponse.ConversationContext, conversationEntry)
	}

	return conversationsResponse, nil
}

func buildGenAiPrompt(conversationContext ConversationResponseEntry) string {
	prompt := fmt.Sprintf("Mention:\n{\n\tText: \"%s\",\n\tTimestamp: \"%s\"\n},\nThreadMessages: [\n",
		conversationContext.MentionText, conversationContext.MentionTimestamp)

	for i, msg := range conversationContext.Messages {
		prompt += fmt.Sprintf("\t{\n\t\tText: \"%s\",\n\t\tTimestamp: \"%s\"\n\t}", msg.Text, msg.Timestamp)
		if i < len(conversationContext.Messages)-1 {
			prompt += ",\n"
		} else {
			prompt += "\n"
		}
	}
	prompt += "]\n"

	promptContext, promptReadError := os.ReadFile("prompt.txt")

	if promptReadError != nil {
		return promptReadError.Error()
	}

	prompt += string(promptContext)
	return prompt
}

func getGenAiSummary(conversationContext ConversationResponseEntry, genAiClient *genai.Client, ctx context.Context) (*genai.GenerateContentResponse, error) {
	/*
		prompt structure:
		{
			Mention:{
				Text: "....",
				Timestamp: "...."
			},
			ThreadMessages: [
				{
					Text: "....",
					Timestamp: "...."
				},
				{
					Text: "....",
					Timestamp: "...."
				},
			]
		}
	*/

	// prepare the message
	genAiPrompt := buildGenAiPrompt(conversationContext)

	genAiGenerateContentResult, genAiGenerateContentError := genAiClient.Models.GenerateContent(
		ctx,
		"gemini-3-flash-preview",
		genai.Text(genAiPrompt),
		nil,
	)
	if genAiGenerateContentError != nil {
		return nil, genAiGenerateContentError
	}

	return genAiGenerateContentResult, nil
}

func main() {
	// load the environment variables
	envFileLoadingError := godotenv.Load()
	if envFileLoadingError != nil {
		log.Fatal("Error loading .env file")
	}

	slackUserToken := os.Getenv("SLACK_USER_TOKEN")
	slackApi := slack.New(slackUserToken)

	geminiApiKey := os.Getenv("GEMINI_API_KEY")

	// GET mentions for the user in the last day
	userId := "U040A3Y6W5Q"
	mentions, getMentionsError := getMentions(slackApi, userId)

	if getMentionsError != nil {
		log.Fatal(getMentionsError)
	}

	// GET the entire conversation for each thread
	conversationsResponse, getConversationsError := getConversation(slackApi, mentions)

	if getConversationsError != nil {
		log.Fatal(getConversationsError)
	}

	// Gemini setup
	ctx := context.Background()
	genAiClient, genAiError := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  geminiApiKey,
		Backend: genai.BackendGeminiAPI,
	})

	if genAiError != nil {
		log.Fatal(genAiError)
	}

	// Query the LLM with the entire context
	for mentionIndex, conversationContext := range conversationsResponse.ConversationContext {

		geminiSummary, getGeminiSummaryError := getGenAiSummary(conversationContext, genAiClient, ctx)

		if getGeminiSummaryError != nil {
			log.Fatal(getGeminiSummaryError)
		}

		if len(geminiSummary.Candidates) > 0 {
			for _, part := range geminiSummary.Candidates[0].Content.Parts {

				cleanedJson := cleanJSON(part.Text)

				var s GenAiResponse
				jsonUnmarshallError := json.Unmarshal([]byte(cleanedJson), &s)

				if jsonUnmarshallError != nil {
					log.Fatal(jsonUnmarshallError)
				}

				// Now you can access fields:
				fmt.Println("-----------------------Index:", mentionIndex+1, "Start-----------------------")
				fmt.Println(s.Summary)
				fmt.Println(s.Actionable)
				fmt.Println(s.ActionRequired)
				fmt.Println(s.Priority)
				fmt.Println("-----------------------Index:", mentionIndex+1, "End-----------------------")
			}
		} else {
			fmt.Println("No candidates in response")
		}
	}

	// Now once the summary is ready, with other field
	// make a array output in the console

	// connect to google sheets file
	// create a new sheet with today's date and dump the data
}
