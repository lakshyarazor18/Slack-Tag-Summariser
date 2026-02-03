package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slack-go/slack"
	"google.golang.org/genai"
)

var dbPool *pgxpool.Pool
var maxMentions = 30

type UniqueMention struct {
	Timestamp string
	ChannelId string
}

type ConversationsResponse struct {
	// I want immutability
	// The struct is not big enough to make a pointer
	// hence using the slice directly for ConversationResponseEntry
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
	MentionPermalink string
	Summary          []string `json:"summary"`
	Actionable       string   `json:"actionable"`
	ActionRequired   []string `json:"action_required"`
	Priority         string   `json:"priority"`
}

type User struct {
	UserID string
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
	yesterday := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	today := time.Now().Format("2006-01-02")
	query := fmt.Sprintf("<@%s> after:%s before:%s", userId, yesterday, today)

	params := slack.SearchParameters{
		Sort:          "timestamp",
		SortDirection: "desc",
		Count:         maxMentions, // taking at max 30 mentions in a day
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
		"gemini-3-pro-preview",
		genai.Text(genAiPrompt),
		nil,
	)
	if genAiGenerateContentError != nil {
		return nil, genAiGenerateContentError
	}

	return genAiGenerateContentResult, nil
}

func sortGenAiResponsesByPriority(responses []GenAiResponse) {
	priorityOrder := map[string]int{
		"P0": 0,
		"P1": 1,
		"P2": 2,
	}
	sort.Slice(responses, func(i, j int) bool {
		pi := priorityOrder[strings.ToUpper(responses[i].Priority)]
		pj := priorityOrder[strings.ToUpper(responses[j].Priority)]
		return pi < pj
	})
}

func processUser(slackApi *slack.Client, genAiClient *genai.Client, ctx context.Context, userId string) ([]GenAiResponse, error) {

	// GET mentions for the user in the last day
	mentions, getMentionsError := getMentions(slackApi, userId)

	if getMentionsError != nil {
		return nil, getMentionsError
	}
	// GET the entire conversation for each thread
	conversationsResponse, getConversationsError := getConversation(slackApi, mentions)

	if getConversationsError != nil {
		return nil, getConversationsError
	}

	// save the response from gemini in a slice of GenAiResponse
	var genAiResponses []GenAiResponse
	// Query the LLM with the entire context
	for _, conversationContext := range conversationsResponse.ConversationContext {

		geminiSummary, getGeminiSummaryError := getGenAiSummary(conversationContext, genAiClient, ctx)

		if getGeminiSummaryError != nil {
			return nil, getGeminiSummaryError
		}

		if len(geminiSummary.Candidates) > 0 {
			for _, part := range geminiSummary.Candidates[0].Content.Parts {

				cleanedJson := cleanJSON(part.Text)

				var s GenAiResponse
				var data map[string]interface{}
				jsonUnmarshallError := json.Unmarshal([]byte(cleanedJson), &data)

				if jsonUnmarshallError != nil {
					return nil, jsonUnmarshallError
				}

				// prepare the GenAiResponse struct
				s.Actionable = data["actionable"].(string)
				s.Priority = data["priority"].(string)
				s.MentionPermalink = conversationContext.MentionPermalink

				if summary, ok := data["summary"].([]interface{}); ok {
					for _, item := range summary {
						s.Summary = append(s.Summary, item.(string))
					}
				}

				if actionRequired, ok := data["action_required"].([]interface{}); ok {
					for _, item := range actionRequired {
						s.ActionRequired = append(s.ActionRequired, item.(string))
					}
				}

				genAiResponses = append(genAiResponses, s)
			}
		}
	}

	sortGenAiResponsesByPriority(genAiResponses)
	return genAiResponses, nil
}

func initDbPool() error {
	databaseUrl := os.Getenv("DATABASE_URL")
	var dbConnectionError error
	dbPool, dbConnectionError = pgxpool.New(context.Background(), databaseUrl)
	if dbConnectionError != nil {
		return dbConnectionError
	}
	return nil
}

func saveUserToDb(userId string) error {

	if dbPool == nil {
		return fmt.Errorf("database pool is not initialized")
	}

	query := `
		INSERT INTO users (user_id)
		VALUES ($1)`

	// Execute using the shared pool
	_, saveUserToDbError := dbPool.Exec(context.Background(), query, userId)
	if saveUserToDbError != nil {
		return saveUserToDbError
	}

	return nil
}

func checkUserInDb(userId string) (bool, error) {

	if dbPool == nil {
		return false, fmt.Errorf("database pool is not initialized")
	}

	query := `
		SELECT COUNT(*) FROM users WHERE user_id = $1`

	var count int
	dbQueryError := dbPool.QueryRow(context.Background(), query, userId).Scan(&count)
	if dbQueryError != nil {
		return false, dbQueryError
	}

	return count > 0, nil
}

func HandleSlackRedirect(w http.ResponseWriter, r *http.Request) {
	// Get the temporary code from the URL query
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing code", http.StatusBadRequest)
		return
	}

	// Exchange code for a permanent token
	// Replace these with your actual Client ID and Secret from Slack Dashboard
	clientID := os.Getenv("SLACK_CLIENT_ID")
	clientSecret := os.Getenv("SLACK_CLIENT_SECRET")

	// The redirect_uri must match EXACTLY what is in your Slack Dashboard
	redirectURI := "https://slack-tag-summariser.onrender.com/slack/oauth/callback"

	resp, err := slack.GetOAuthV2Response(http.DefaultClient, clientID, clientSecret, code, redirectURI)
	if err != nil {
		http.Error(w, "Failed to authenticate with Slack", http.StatusInternalServerError)
		return
	}

	//Extract the data for your Supabase table
	userID := resp.AuthedUser.ID

	// check before save
	userExists, checkUserError := checkUserInDb(userID)

	if checkUserError != nil {
		log.Println("User check failed:", checkUserError)
		http.Error(w, "Failed to check user in database", http.StatusInternalServerError)
		return
	}

	if userExists {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Already Registered!</h1><p>The summarizer is already active for your account.</p>")
		return
	}

	saveUserError := saveUserToDb(userID)

	if saveUserError != nil {
		log.Println("User save failed:", saveUserError)
		http.Error(w, "Failed to save user to database", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, "<h1>Success!</h1><p>The summarizer is now active for your account.</p>")
}

func getInstalledUsers() ([]User, error) {
	if dbPool == nil {
		return nil, fmt.Errorf("database pool is not initialized")
	}

	query := `SELECT user_id FROM users`

	rows, dbQueryError := dbPool.Query(context.Background(), query)
	if dbQueryError != nil {
		return nil, dbQueryError
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.UserID); err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return users, nil
}

func formatGenAiResponsesVertical(responses []GenAiResponse) string {
	var b strings.Builder

	for i, r := range responses {
		// 1. Header with Emoji & Link
		b.WriteString(fmt.Sprintf("🔗 *Mention Link:* <%s|Click Here> |\n", r.MentionPermalink))

		// 2. Priority-based Emoji logic
		priorityEmoji := "⚪" // Default
		switch strings.ToUpper(r.Priority) {
		case "P0", "P1":
			priorityEmoji = "🚨"
		case "P2":
			priorityEmoji = "⚠️"
		case "P3":
			priorityEmoji = "🔵"
		}

		// Actionable Emoji
		actionEmoji := "✅"
		if strings.ToLower(r.Actionable) == "no" {
			actionEmoji = "➖"
		}

		// 3. Status Row
		b.WriteString(fmt.Sprintf("%s *Actionable:* %s.     %s *Priority:* `%s`\n", actionEmoji, r.Actionable, priorityEmoji, r.Priority))

		// 4. Summary Section (with a nice header emoji)
		b.WriteString("\n📝 *Summary*\n")
		for j, s := range r.Summary {
			b.WriteString(fmt.Sprintf("  %d. %s\n", j+1, s))
		}

		// 5. Action Required Section
		if len(r.ActionRequired) > 0 {
			b.WriteString("\n🛠️ *Action Required*\n")
			for _, a := range r.ActionRequired {
				b.WriteString(fmt.Sprintf("  • %s\n", a)) // Using bullets for actions for variety
			}
		}

		// 6. Styled Divider
		if i < len(responses)-1 {
			b.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
		}
	}

	return b.String()
}

func sendSlackDm(slackClient *slack.Client, userId string, processUserResult []GenAiResponse) (bool, error) {
	msg := formatGenAiResponsesVertical(processUserResult)

	fmt.Println(msg)

	_, _, sendSlackDmError := slackClient.PostMessage(
		userId,
		slack.MsgOptionText(msg, false),
	)

	if sendSlackDmError != nil {
		return false, sendSlackDmError
	}

	return true, nil
}

func main() {

	//err := godotenv.Load()
	//if err != nil {
	//	log.Fatal("Error loading .env file")
	//}

	//slackUserToken := os.Getenv("SLACK_USER_TOKEN")
	//slackApi := slack.New(slackUserToken)
	//geminiApiKey := os.Getenv("GEMINI_API_KEY")

	// Gemini setup
	//geminiApiKey = os.Getenv("GEMINI_API_KEY")
	//ctx := context.Background()
	//
	//genAiClient, genAiError := genai.NewClient(ctx, &genai.ClientConfig{
	//	APIKey:  geminiApiKey,
	//	Backend: genai.BackendGeminiAPI,
	//})
	//
	//if genAiError != nil {
	//	log.Fatal(genAiError)
	//}

	dbInitialisationError := initDbPool()

	if dbInitialisationError != nil {
		log.Fatal("Failed to initialise DB:", dbInitialisationError)
	}

	http.HandleFunc("/slack/oauth/callback", HandleSlackRedirect)

	// Health endpoint
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Service running"))
	})

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			resp, err := http.Get("https://slack-tag-summariser.onrender.com/")
			if err != nil {
				log.Println("Health check failed:", err)
			} else {
				resp.Body.Close()
				log.Println("Health check successful")
			}
			<-ticker.C
		}
	}()

	port := "8080"
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
