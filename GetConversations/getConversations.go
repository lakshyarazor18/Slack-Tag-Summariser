package GetConversations

import (
	"log"
	"net/url"

	"slack-tag-summariser/Models"

	"github.com/slack-go/slack"
)

type ConversationsResponse = Models.ConversationsResponse
type ConversationResponseEntry = Models.ConversationResponseEntry
type ThreadMessage = Models.ThreadMessage

func getConversations(SlackClient *slack.Client, filteredMentions []slack.SearchMessage) (*ConversationsResponse, error) {

	// Rule: # of mentions = # of conversations
	conversationsResponse := &ConversationsResponse{}

	// we will iterate through each mention in the mentions array
	for _, mention := range filteredMentions {
		conversationEntry := GetConversation(SlackClient, mention)
		conversationsResponse.ConversationContext = append(conversationsResponse.ConversationContext, conversationEntry)
	}

	return conversationsResponse, nil
}

func GetConversation(SlackClient *slack.Client, mention slack.SearchMessage) ConversationResponseEntry {

	// to generate the response I need to make a ConversationResponseEntry
	// currently this is empty with default values
	var conversationEntry ConversationResponseEntry

	// for each we have the channelId and threadTs
	channelId := mention.Channel.ID
	threadTs := mention.Timestamp

	parsedUrl, urlParseError := url.Parse(mention.Permalink)

	if urlParseError != nil {
		log.Printf("GetConversations:getConversation#Error while parsing the mentionLink: %s", urlParseError.Error())
		return conversationEntry
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
		log.Printf("GetConversations:getConversation#Error while fetching the conversation replies: %s", getConversationRepliesError.Error())
		return conversationEntry
	}

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
	return conversationEntry
}
