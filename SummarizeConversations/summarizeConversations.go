package SummarizeConversations

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"slack-tag-summariser/Models"

	"google.golang.org/genai"
)

type GenAiResponse = Models.GenAiResponse
type ConversationResponseEntry = Models.ConversationResponseEntry
type ConversationsResponse = Models.ConversationsResponse

func cleanJSON(input string) string {
	input = strings.TrimSpace(input)

	// Remove ```json and ``` if present
	input = strings.TrimPrefix(input, "```json")
	input = strings.TrimPrefix(input, "```")
	input = strings.TrimSuffix(input, "```")

	return strings.TrimSpace(input)
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

func SortGenAiResponsesByPriority(responses []GenAiResponse) {
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

func summarizeAllConversationsWithGenAi(
	conversationsResponse *Models.ConversationsResponse,
	genAiClient *genai.Client,
	ctx context.Context,
	genAiResponses []GenAiResponse) error {

	// Query the LLM with the entire context
	for _, conversationContext := range conversationsResponse.ConversationContext {
		genAiRes := SummarizeSingleConversation(conversationContext, genAiClient, ctx)
		genAiResponses = append(genAiResponses, genAiRes)
	}
	sortGenAiResponsesByPriority(genAiResponses)
	return nil
}

func SummarizeSingleConversation(
	conversationContext ConversationResponseEntry,
	genAiClient *genai.Client,
	ctx context.Context) GenAiResponse {
	geminiSummary, getGeminiSummaryError := getGenAiSummary(conversationContext, genAiClient, ctx)

	var s GenAiResponse
	if getGeminiSummaryError != nil {
		log.Printf("SummarizeConversations:SummarizeSingleConversation#Error getting gemini summary: %s", getGeminiSummaryError.Error())
		return s
	}

	if len(geminiSummary.Candidates) > 0 {
		for _, part := range geminiSummary.Candidates[0].Content.Parts {

			cleanedJson := cleanJSON(part.Text)

			var data map[string]interface{}
			jsonUnmarshallError := json.Unmarshal([]byte(cleanedJson), &data)

			if jsonUnmarshallError != nil {
				log.Printf("SummarizeConversations:SummarizeSingleConversation#Error unmarshalling json: %s", jsonUnmarshallError.Error())
				return s
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
		}
	}
	return s
}
