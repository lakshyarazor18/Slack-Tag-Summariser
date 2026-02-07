package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"slack-tag-summariser/GetConversations"
	"slack-tag-summariser/GetMentions"
	"slack-tag-summariser/Models"
	"slack-tag-summariser/PublishToSlack"
	"slack-tag-summariser/Repo"
	"slack-tag-summariser/SummarizeConversations"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slack-go/slack"
	"google.golang.org/genai"
)

type ConversationResponseEntry = Models.ConversationResponseEntry

var dbPool *pgxpool.Pool

type GenAiResponse = Models.GenAiResponse

func processUser(slackApi *slack.Client, slackBotApi *slack.Client, genAiClient *genai.Client, ctx context.Context, userId string) (bool, error) {

	// GET mentions for the user in the last day
	mentions, getMentionsError := GetMentions.GetMentions(slackApi, userId)

	if getMentionsError != nil {
		return false, getMentionsError
	}
	// make a channel to save the threads for each mention to get asynchronously
	conversationsChan := make(chan ConversationResponseEntry, len(mentions))
	// initialise a wait group to wait for all the go routines to finish
	var completeConversationResponse sync.WaitGroup

	// GET the entire conversation for each thread
	for _, mention := range mentions {
		completeConversationResponse.Add(1)
		go func(m slack.SearchMessage) {
			// done is added to decrement the count the wait group once the go routine is done executing
			defer completeConversationResponse.Done()

			// we will get the conversation response for each mention
			conversationResponse := GetConversations.GetConversation(slackApi, m)

			// save the conversation response in the channel
			conversationsChan <- conversationResponse
		}(mention)
	}

	// wait for all the go routines to finish
	completeConversationResponse.Wait()
	// once all the go routines are finished we can close the channel
	// this is done so that we don't have a deadlock when ranging over the channel
	close(conversationsChan)

	// Save the response from genAi in a slice of GenAiResponse
	var genAiResponses []GenAiResponse
	// make a summary channel to save the genAi responses for each conversation to get asynchronously
	genAiSummaryChan := make(chan GenAiResponse, len(mentions))

	// initialise a wait group to wait for all the go routines to finish GenAIResponse
	var completeGenAiResponse sync.WaitGroup

	// iterate through the channel and process the AI response
	for conversationContext := range conversationsChan {
		// increase the counter for the wait group as we are starting a new go routine
		completeGenAiResponse.Add(1)
		go func(cc ConversationResponseEntry, genAiClient *genai.Client, ctx context.Context) {
			// done is added to decrement the count the wait group once the go routine is done executing
			defer completeGenAiResponse.Done()

			// we will get the GenAI response for each conversation context
			genAiResponse := SummarizeConversations.SummarizeSingleConversation(cc, genAiClient, ctx)

			// save the genAi response in the channel
			genAiSummaryChan <- genAiResponse
		}(conversationContext, genAiClient, ctx)
	}

	completeGenAiResponse.Wait()
	close(genAiSummaryChan)

	// iterate the genAiSummaryChan to get the summaries for each conversation
	// save it in the genAiResponses slice
	for genAiSummary := range genAiSummaryChan {
		genAiResponses = append(genAiResponses, genAiSummary)
	}

	// sort the GenAI responses by priority before sending it to the user
	SummarizeConversations.SortGenAiResponsesByPriority(genAiResponses)

	// finally we have the summaries for the user now we need to publish it to them in slack DM
	sendSlackDmRes, sendSlackDmErr := PublishToSlack.SendSlackDm(slackBotApi, userId, genAiResponses)

	if sendSlackDmErr != nil {
		return false, sendSlackDmErr
	}
	return sendSlackDmRes, nil
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
	deploymentBaseURI := os.Getenv("DEPLOYMENT_BASE_URI")
	slackRedirectURI := fmt.Sprintf("%sslack/oauth/callback", deploymentBaseURI)

	resp, err := slack.GetOAuthV2Response(http.DefaultClient, clientID, clientSecret, code, slackRedirectURI)
	if err != nil {
		http.Error(w, "Failed to authenticate with Slack", http.StatusInternalServerError)
		return
	}

	//Extract the data for your DB table
	userID := resp.AuthedUser.ID

	// check before save
	userExists, checkUserError := Repo.CheckUserInDb(userID, dbPool)

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

	saveUserError := Repo.SaveUserToDb(userID, dbPool)

	if saveUserError != nil {
		log.Println("User save failed:", saveUserError)
		http.Error(w, "Failed to save user to database", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, "<h1>Success!</h1><p>The summarizer is now active for your account.</p>")
}

func main() {

	//err := godotenv.Load()
	//if err != nil {
	//	log.Fatal("Error loading .env file")
	//}
	deploymentBaseURI := os.Getenv("DEPLOYMENT_BASE_URI")
	//slackUserToken := os.Getenv("SLACK_USER_TOKEN")
	//slackBotToken := os.Getenv("SLACK_BOT_TOKEN")
	//slackApi := slack.New(slackUserToken)
	//slackBotApi := slack.New(slackBotToken)
	//geminiApiKey := os.Getenv("GEMINI_API_KEY")
	//
	////Gemini setup
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

	//_, processUserErr := processUser(slackApi, slackBotApi, genAiClient, ctx, "U08J3EBHG4C")
	//
	//if processUserErr != nil {
	//	log.Fatal("Process User Error:", processUserErr)
	//}

	dbInitialisationError := Repo.InitDbPool(&dbPool)

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
			resp, err := http.Get(deploymentBaseURI)
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
