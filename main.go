package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/slack-go/slack"
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

func main() {
	// load the environment variables
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	slackUserToken := os.Getenv("SLACK_USER_TOKEN")
	slackApi := slack.New(slackUserToken)

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

	for _, conversationContext := range conversationsResponse.ConversationContext {
		fmt.Println(conversationContext.MentionText)
		fmt.Println(conversationContext.MentionTimestamp)
		fmt.Println(conversationContext.MentionChannelId)
		fmt.Println(conversationContext.MentionPermalink)

		for _, msg := range conversationContext.Messages {
			fmt.Println("-----------------------------------------------------")
			fmt.Println("Timestamp:", msg.Timestamp, ", Message text:", msg.Text)
			fmt.Println("-----------------------------------------------------")
		}

		break
	}

	// provide that conversation to the LLM to get a summary

	// Now once the summary is ready, with other field
	// make a array output in the console

	// connect to google sheets file
	// create a new sheet with today's date and dump the data
}
